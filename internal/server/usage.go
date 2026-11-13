package server

import (
	"encoding/csv"
	"mime"
	"net/http"
	"strconv"
	"sync"
	"time"
)

// License usage history: for every license and day, the highest number of
// people and computers that had its fonts, counted like the seats (admins
// left out where the license doesn't count them). Recorded by the janitor (every
// ten minutes) and after changes, so short peaks are kept too. Foundry
// audits ask for exactly this: "how many installs over the last year?"

var usageMu sync.Mutex

func usageDay(t time.Time) string { return t.UTC().Format(dateLayout) }

// recordUsage stores today's usage, keeping the day's maximum.
func (s *Server) recordUsage() {
	usageMu.Lock()
	defer usageMu.Unlock()
	_, err := s.db.Exec(`INSERT INTO license_usage (day, license_id, users, devices, machines)
		SELECT ?, l.id,
			(SELECT COUNT(DISTINCT a.user_id) FROM user_fonts a JOIN fonts f ON f.id = a.font_id JOIN users au ON au.id = a.user_id
				WHERE f.license_id = l.id AND (l.count_admins = 1 OR au.is_admin = 0)),
			(SELECT COUNT(DISTINCT df.device_id) FROM device_fonts df JOIN fonts f ON f.sha256 = df.sha256
				JOIN devices dd ON dd.id = df.device_id JOIN users du ON du.id = dd.user_id
				WHERE f.license_id = l.id AND (l.count_admins = 1 OR du.is_admin = 0 OR dd.platform LIKE '%-machine')),
			(SELECT COUNT(DISTINCT df.device_id) FROM device_fonts df JOIN fonts f ON f.sha256 = df.sha256
				JOIN devices d ON d.id = df.device_id WHERE f.license_id = l.id AND d.platform LIKE '%-machine')
		FROM licenses l WHERE 1
		ON CONFLICT (day, license_id) DO UPDATE SET
			users = MAX(users, excluded.users), devices = MAX(devices, excluded.devices), machines = MAX(machines, excluded.machines)`,
		usageDay(time.Now()))
	if err != nil {
		s.log.Warn("record license usage", "err", err)
	}
}

var lastUsageRecord struct {
	sync.Mutex
	t time.Time
}

// usageChanged records usage after activations, collection changes and
// device reports, at most once a minute.
func (s *Server) usageChanged() {
	lastUsageRecord.Lock()
	if time.Since(lastUsageRecord.t) < time.Minute {
		lastUsageRecord.Unlock()
		return
	}
	lastUsageRecord.t = time.Now()
	lastUsageRecord.Unlock()
	go s.recordUsage()
}

type usagePoint struct {
	Day      string `json:"day"`
	Users    int    `json:"users"`
	Devices  int    `json:"devices"`
	Machines int    `json:"machines"`
}

// peaks returns, per license, the highest daily usage of the last 365 days.
func (s *Server) peaks() map[int64]usagePoint {
	out := map[int64]usagePoint{}
	rows, err := s.db.Query(`SELECT license_id, MAX(users), MAX(devices), MAX(machines) FROM license_usage
		WHERE day >= ? GROUP BY license_id`, usageDay(time.Now().AddDate(-1, 0, 0)))
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var p usagePoint
		rows.Scan(&id, &p.Users, &p.Devices, &p.Machines)
		out[id] = p
	}
	return out
}

func (s *Server) usageHistory(licenseID int64, days int) []usagePoint {
	q := `SELECT day, users, devices, machines FROM license_usage WHERE day >= ?`
	args := []any{usageDay(time.Now().AddDate(0, 0, -days))}
	if licenseID > 0 {
		q += ` AND license_id = ?`
		args = append(args, licenseID)
	}
	rows, err := s.db.Query(q+` ORDER BY day`, args...)
	out := []usagePoint{}
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var p usagePoint
		rows.Scan(&p.Day, &p.Users, &p.Devices, &p.Machines)
		out = append(out, p)
	}
	return out
}

// GET /api/licenses/{id}/history?days=365
func (s *Server) handleLicenseHistory(w http.ResponseWriter, r *http.Request, u *authUser) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, 400, "bad id")
		return
	}
	days := 365
	if d, err := strconv.Atoi(r.URL.Query().Get("days")); err == nil && d > 0 && d <= 3650 {
		days = d
	}
	writeJSON(w, 200, s.usageHistory(id, days))
}

// GET /api/licenses/history.csv – daily usage of every license, last year
func (s *Server) handleUsageCSV(w http.ResponseWriter, r *http.Request, u *authUser) {
	rows, err := s.db.Query(`SELECT lu.day, l.name, l.seats, l.seat_unit, lu.users, lu.devices, lu.machines
		FROM license_usage lu JOIN licenses l ON l.id = lu.license_id WHERE lu.day >= ?
		ORDER BY lu.day, l.name COLLATE NOCASE`, usageDay(time.Now().AddDate(-1, 0, 0)))
	if err != nil {
		s.internalErr(w, err)
		return
	}
	defer rows.Close()
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment",
		map[string]string{"filename": "typehaus-license-usage-" + time.Now().Format(dateLayout) + ".csv"}))
	w.Write([]byte("\ufeff"))
	cw := csv.NewWriter(w)
	cw.Write([]string{"Day", "License", "Seats", "Seat unit", "Users (max)", "Devices (max)", "All-users computers (max)"})
	for rows.Next() {
		var day, name, unit string
		var seats, users, devices, machines int
		rows.Scan(&day, &name, &seats, &unit, &users, &devices, &machines)
		st := "unlimited"
		if seats > 0 {
			st = strconv.Itoa(seats)
		}
		cw.Write([]string{day, name, st, unit, strconv.Itoa(users), strconv.Itoa(devices), strconv.Itoa(machines)})
	}
	cw.Flush()
	s.audit(u, "license.export", "usage history")
}
