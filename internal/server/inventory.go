package server

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"

	"github.com/4nonX/typehaus/internal/protocol"
)

// Inventory scan (opt-in, TYPEHAUS_INVENTORY=true): agents report the fonts
// installed on a computer outside typehaus – copied in by hand, installed by
// other software. Copies of library fonts that are not open source are what
// an audit asks about: they use a license without being counted.

const maxInventory = 20000

func (s *Server) handleAgentInventory(w http.ResponseWriter, r *http.Request, d *authDevice) {
	if !s.cfg.Inventory {
		writeErr(w, http.StatusConflict, "the inventory scan is switched off on this server")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<20)
	var rep protocol.InventoryReport
	if err := json.NewDecoder(r.Body).Decode(&rep); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if len(rep.Fonts) > maxInventory {
		rep.Fonts = rep.Fonts[:maxInventory]
	}
	tx, err := s.db.Begin()
	if err != nil {
		s.internalErr(w, err)
		return
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM device_inventory WHERE device_id = ?`, d.ID); err != nil {
		s.internalErr(w, err)
		return
	}
	stmt, err := tx.Prepare(`INSERT OR IGNORE INTO device_inventory (device_id, path, scope, family, style, full_name, ps_name, kind, sha256, size)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		s.internalErr(w, err)
		return
	}
	defer stmt.Close()
	for _, f := range rep.Fonts {
		scope := "user"
		if f.Scope == "computer" {
			scope = "computer"
		}
		if _, err := stmt.Exec(d.ID, trunc(f.Path, 1000), scope, trunc(f.Family, 200), trunc(f.Style, 200), trunc(f.FullName, 300),
			trunc(f.PostScript, 200), trunc(f.Kind, 20), strings.ToLower(trunc(f.SHA256, 64)), max(f.Size, 0)); err != nil {
			s.internalErr(w, err)
			return
		}
	}
	if _, err := tx.Exec(`UPDATE devices SET inventory_at = ? WHERE id = ?`, now(), d.ID); err != nil {
		s.internalErr(w, err)
		return
	}
	if err := tx.Commit(); err != nil {
		s.internalErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]int{"fonts": len(rep.Fonts)})
}

type inventoryCopy struct {
	DeviceID   int64  `json:"device_id"`
	Device     string `json:"device"`
	User       string `json:"user"`
	Path       string `json:"path"`
	Scope      string `json:"scope"`
	Style      string `json:"style"`
	Exact      bool   `json:"exact"` // byte-identical to a library file
	ScannedAt  int64  `json:"scanned_at"`
	FamilyName string `json:"-"`
}

type inventoryFinding struct {
	Family    string          `json:"family"`
	LicenseID *int64          `json:"license_id"`
	License   string          `json:"license"`
	Devices   int             `json:"devices"`
	Copies    []inventoryCopy `json:"copies"`
}

// handleInventory lists copies of library fonts found outside typehaus.
// Open-source families are left out: copying them is allowed.
func (s *Server) handleInventory(w http.ResponseWriter, r *http.Request, u *authUser) {
	out := struct {
		Enabled   bool               `json:"enabled"`
		Scanned   int                `json:"devices_scanned"`
		Unmanaged int                `json:"unmanaged_files"`
		Findings  []inventoryFinding `json:"findings"`
	}{Enabled: s.cfg.Inventory, Findings: []inventoryFinding{}}
	s.db.QueryRow(`SELECT COUNT(*) FROM devices WHERE inventory_at IS NOT NULL AND EXISTS (SELECT 1 FROM device_inventory i WHERE i.device_id = devices.id)`).Scan(&out.Scanned)
	s.db.QueryRow(`SELECT COUNT(*) FROM device_inventory`).Scan(&out.Unmanaged)

	// library families with their license (the newest file decides)
	rows, err := s.db.Query(`SELECT i.device_id, d.name, us.username, i.path, i.scope, i.style, d.inventory_at,
		EXISTS(SELECT 1 FROM fonts f WHERE f.sha256 = i.sha256),
		COALESCE((SELECT f.family FROM fonts f WHERE f.sha256 = i.sha256 LIMIT 1),
			(SELECT f.family FROM fonts f WHERE f.family = i.family COLLATE NOCASE LIMIT 1))
		FROM device_inventory i JOIN devices d ON d.id = i.device_id JOIN users us ON us.id = d.user_id
		WHERE EXISTS (SELECT 1 FROM fonts f WHERE f.sha256 = i.sha256)
		   OR EXISTS (SELECT 1 FROM fonts f WHERE f.family = i.family COLLATE NOCASE)
		ORDER BY i.family COLLATE NOCASE, d.name, i.path`)
	if err != nil {
		s.internalErr(w, err)
		return
	}
	var copies []inventoryCopy
	for rows.Next() {
		var c inventoryCopy
		if err := rows.Scan(&c.DeviceID, &c.Device, &c.User, &c.Path, &c.Scope, &c.Style, &c.ScannedAt, &c.Exact, &c.FamilyName); err != nil {
			rows.Close()
			s.internalErr(w, err)
			return
		}
		copies = append(copies, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		s.internalErr(w, err)
		return
	}

	type lic struct {
		id   *int64
		name string
		open bool
	}
	licOf := map[string]lic{}
	byFamily := map[string]*inventoryFinding{}
	devs := map[string]map[int64]bool{}
	for _, c := range copies {
		key := strings.ToLower(c.FamilyName)
		l, ok := licOf[key]
		if !ok {
			var id *int64
			var name, kind string
			var lid int64
			err := s.db.QueryRow(`SELECT l.id, l.name, l.kind FROM fonts f JOIN licenses l ON l.id = f.license_id
				WHERE f.family = ? COLLATE NOCASE ORDER BY f.id DESC LIMIT 1`, c.FamilyName).Scan(&lid, &name, &kind)
			if err == nil {
				id = &lid
			}
			l = lic{id: id, name: name, open: kind == "open"}
			licOf[key] = l
		}
		if l.open {
			continue
		}
		f := byFamily[key]
		if f == nil {
			f = &inventoryFinding{Family: c.FamilyName, LicenseID: l.id, License: l.name}
			byFamily[key] = f
			devs[key] = map[int64]bool{}
		}
		f.Copies = append(f.Copies, c)
		devs[key][c.DeviceID] = true
	}
	for key, f := range byFamily {
		f.Devices = len(devs[key])
		out.Findings = append(out.Findings, *f)
	}
	sort.Slice(out.Findings, func(i, j int) bool {
		if out.Findings[i].Devices != out.Findings[j].Devices {
			return out.Findings[i].Devices > out.Findings[j].Devices
		}
		return strings.ToLower(out.Findings[i].Family) < strings.ToLower(out.Findings[j].Family)
	})
	writeJSON(w, 200, out)
}
