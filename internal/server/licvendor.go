package server

import (
	"database/sql"
	"errors"
	"net/http"
	"os"
	"sort"
	"strings"

	"github.com/4nonX/typehaus/internal/fontmeta"
)

// ---- commercial foundry recognition ------------------------------------------
//
// Open-source licenses are named in the font and assigned automatically
// (autoLicense). Commercial fonts are different: the file only tells us who
// made it, not what was bought. They are therefore only suggested, grouped by
// family, and the admin decides; the license record that results asks for the
// seat count, term and invoice.

// commercialVendors maps OS/2 vendor IDs and names found in the copyright,
// trademark or manufacturer fields to a foundry.
var commercialVendors = []struct {
	vendor string
	ids    []string // OS/2 achVendID, upper case, trimmed
	names  []string // lower-case substrings
}{
	{"Adobe", []string{"ADBE", "ADBO"}, []string{"adobe systems", "adobe inc"}},
	{"Monotype", []string{"MONO", "TMC"}, []string{"monotype"}},
	{"Linotype", []string{"LINO"}, []string{"linotype"}},
	{"ITC", []string{"ITC"}, []string{"international typeface corporation"}},
	{"URW Type Foundry", []string{"URW"}, []string{"urw type foundry", "urw++"}},
	{"Microsoft", []string{"MS", "MSFT"}, []string{"microsoft corporation"}},
	{"Apple", []string{"APPL"}, []string{"apple inc", "apple computer"}},
	{"Bitstream", []string{"BITS"}, []string{"bitstream inc"}},
	{"Berthold", []string{"BERT"}, []string{"berthold"}},
	{"Font Bureau", []string{"FBI"}, []string{"font bureau"}},
	{"Hoefler&Co.", []string{"H&FJ"}, []string{"hoefler"}},
	{"FontShop", []string{"FSI"}, []string{"fontshop international"}},
	{"Elsner+Flake", []string{"EF"}, []string{"elsner+flake", "elsner & flake"}},
	{"Bigelow & Holmes", []string{"B&H"}, []string{"bigelow & holmes", "bigelow and holmes"}},
	{"Typodermic", []string{"TYPO"}, []string{"typodermic"}},
	{"Fontfabric", []string{"FTFB"}, []string{"fontfabric"}},
	{"Fontsmith", []string{"FSMI"}, []string{"fontsmith"}},
	{"Commercial Type", []string{"CTYP"}, []string{"commercial type"}},
	{"Dalton Maag", []string{"DAMA"}, []string{"dalton maag"}},
	{"Emigre", []string{"EMGR"}, []string{"emigre"}},
	{"House Industries", []string{"HOUS"}, []string{"house industries"}},
}

// eulaPhrases mark a license description as a commercial end-user license
// when the foundry itself is not in the list above.
var eulaPhrases = []string{"end user license", "end-user license", "eula", "license agreement",
	"must be licensed", "purchase a license", "may not be used without"}

// detectCommercial names the foundry of a commercially licensed face, with the
// metadata that gave it away. It returns "" when the face carries an open
// license or nothing usable.
func detectCommercial(f fontmeta.Face) (vendor, reason string) {
	if detectOpenLicense(f.License, f.LicenseURL) != nil {
		return "", ""
	}
	if vid := strings.ToUpper(strings.TrimSpace(f.VendorID)); vid != "" {
		for _, c := range commercialVendors {
			for _, id := range c.ids {
				if vid == id {
					return c.vendor, "vendor ID " + vid
				}
			}
		}
	}
	owner := strings.ToLower(f.Copyright + "\n" + f.Trademark + "\n" + f.Manufacturer)
	for _, c := range commercialVendors {
		for _, n := range c.names {
			if strings.Contains(owner, n) {
				return c.vendor, "copyright / trademark names " + c.vendor
			}
		}
	}
	lic := strings.ToLower(f.License + "\n" + f.LicenseURL)
	for _, p := range eulaPhrases {
		if strings.Contains(lic, p) {
			v := strings.TrimSpace(f.Manufacturer)
			if v == "" {
				v = strings.TrimSpace(f.VendorID)
			}
			if v == "" {
				v = "Unknown foundry"
			}
			return v, "license text reads like an end-user license"
		}
	}
	return "", ""
}

type vendorSuggestion struct {
	Family  string  `json:"family"`
	FontIDs []int64 `json:"font_ids"`
	Vendor  string  `json:"vendor"`
	Reason  string  `json:"reason"`
	// LicenseID is the existing commercial license of that vendor, if there is
	// exactly one; otherwise applying creates "<vendor> (commercial)".
	LicenseID   *int64 `json:"license_id"`
	LicenseName string `json:"license_name"`
}

// handleLicenseSuggestions lists unassigned families whose files name a
// commercial foundry. Vendor ID and trademark are stored at upload; fonts
// uploaded before that are read from their files once and then stored too.
func (s *Server) handleLicenseSuggestions(w http.ResponseWriter, r *http.Request, u *authUser) {
	rows, err := s.db.Query(`SELECT id, sha256, face_index, family, copyright, manufacturer, license_text, license_url,
		vendor_id, trademark, meta_checked FROM fonts WHERE license_id IS NULL ORDER BY family COLLATE NOCASE, id`)
	if err != nil {
		s.internalErr(w, err)
		return
	}
	type face struct {
		id      int64
		sha     string
		index   int
		family  string
		meta    fontmeta.Face
		checked bool
	}
	var faces []face
	for rows.Next() {
		var f face
		rows.Scan(&f.id, &f.sha, &f.index, &f.family, &f.meta.Copyright, &f.meta.Manufacturer, &f.meta.License, &f.meta.LicenseURL,
			&f.meta.VendorID, &f.meta.Trademark, &f.checked)
		faces = append(faces, f)
	}
	rows.Close()

	parsed := map[string]*fontmeta.Info{}
	type tally struct {
		ids     []int64
		votes   map[string]int
		reasons map[string]string
	}
	fams := map[string]*tally{}
	var order []string
	for _, f := range faces {
		meta := f.meta
		if !f.checked {
			info, seen := parsed[f.sha]
			if !seen {
				if b, err := os.ReadFile(s.blobPath(f.sha)); err == nil {
					info, _ = fontmeta.Parse(b)
				}
				parsed[f.sha] = info
			}
			if info != nil && f.index < len(info.Faces) {
				meta.VendorID = info.Faces[f.index].VendorID
				meta.Trademark = info.Faces[f.index].Trademark
			}
			// stored even when empty or unreadable, so the file is never read again
			s.db.Exec(`UPDATE fonts SET vendor_id = ?, trademark = ?, meta_checked = 1 WHERE id = ?`,
				meta.VendorID, meta.Trademark, f.id)
		}
		t := fams[f.family]
		if t == nil {
			t = &tally{votes: map[string]int{}, reasons: map[string]string{}}
			fams[f.family] = t
			order = append(order, f.family)
		}
		t.ids = append(t.ids, f.id)
		if v, why := detectCommercial(meta); v != "" {
			t.votes[v]++
			t.reasons[v] = why
		}
	}
	out := []vendorSuggestion{}
	for _, fam := range order {
		t := fams[fam]
		if len(t.votes) == 0 {
			continue
		}
		vendors := make([]string, 0, len(t.votes))
		for v := range t.votes {
			vendors = append(vendors, v)
		}
		sort.Slice(vendors, func(i, j int) bool {
			if t.votes[vendors[i]] != t.votes[vendors[j]] {
				return t.votes[vendors[i]] > t.votes[vendors[j]]
			}
			return vendors[i] < vendors[j]
		})
		v := vendors[0]
		sug := vendorSuggestion{Family: fam, FontIDs: t.ids, Vendor: v, Reason: t.reasons[v], LicenseName: v + " (commercial)"}
		if id, name, ok := s.vendorLicense(v); ok {
			sug.LicenseID, sug.LicenseName = &id, name
		}
		out = append(out, sug)
	}
	writeJSON(w, 200, out)
}

// vendorLicense finds the commercial license of a vendor when there is exactly
// one; with several (different products) the admin has to pick by hand.
func (s *Server) vendorLicense(vendor string) (int64, string, bool) {
	rows, err := s.db.Query(`SELECT id, name FROM licenses WHERE kind = 'commercial' AND vendor = ? COLLATE NOCASE LIMIT 2`, vendor)
	if err != nil {
		return 0, "", false
	}
	defer rows.Close()
	var id int64
	var name string
	n := 0
	for rows.Next() {
		rows.Scan(&id, &name)
		n++
	}
	return id, name, n == 1
}

// handleApplyLicenseSuggestions assigns the families the admin ticked: to the
// vendor's existing license, or to a new "<vendor> (commercial)" record that
// asks for seats, term and invoice. Fonts that got a license in the meantime
// are left alone.
func (s *Server) handleApplyLicenseSuggestions(w http.ResponseWriter, r *http.Request, u *authUser) {
	var req struct {
		Items []struct {
			FontIDs []int64 `json:"font_ids"`
			Vendor  string  `json:"vendor"`
		} `json:"items"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	assigned := 0
	created := []string{}
	for _, it := range req.Items {
		vendor := strings.TrimSpace(it.Vendor)
		if vendor == "" || len(it.FontIDs) == 0 {
			continue
		}
		lid, _, ok := s.vendorLicense(vendor)
		if !ok {
			name := vendor + " (commercial)"
			err := s.db.QueryRow(`SELECT id FROM licenses WHERE name = ? COLLATE NOCASE ORDER BY id LIMIT 1`, name).Scan(&lid)
			if errors.Is(err, sql.ErrNoRows) {
				res, err := s.db.Exec(`INSERT INTO licenses (name, vendor, kind, rights, notes, created_at)
					VALUES (?, ?, 'commercial', 'desktop', ?, ?)`, name, vendor,
					"Created from the fonts' embedded copyright and vendor information. Enter the seats, term, allowed uses and invoice.", now())
				if err != nil {
					s.internalErr(w, err)
					return
				}
				lid, _ = res.LastInsertId()
				created = append(created, name)
			} else if err != nil {
				s.internalErr(w, err)
				return
			}
		}
		res, err := s.db.Exec(`UPDATE fonts SET license_id = ? WHERE license_id IS NULL AND id IN (`+placeholders(len(it.FontIDs))+`)`,
			append([]any{lid}, int64Args(it.FontIDs)...)...)
		if err != nil {
			s.internalErr(w, err)
			return
		}
		n, _ := res.RowsAffected()
		assigned += int(n)
	}
	if assigned > 0 {
		s.audit(u, "license.detect", "%d font(s) assigned to commercial licenses", assigned)
	}
	writeJSON(w, 200, map[string]any{"assigned": assigned, "created": created})
}
