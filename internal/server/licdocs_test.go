package server

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"os"
	"testing"
)

func replaceDoc(t *testing.T, c *client, licID, docID int64, name string, data []byte) int {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("file", name)
	fw.Write(data)
	mw.Close()
	req, _ := http.NewRequest("PUT", c.base+"/api/licenses/"+itoa(licID)+"/documents/"+itoa(docID), &buf)
	req.Header.Set("X-Typehaus", "1")
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := c.hc.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

// License documents: upload (type read from the name), list, change type,
// rename, replace the file, visibility for users, delete.
func TestLicenseDocumentsCRUD(t *testing.T) {
	s, ts, admin := newTestServer(t)
	var lic map[string]int64
	admin.do("POST", "/api/licenses", map[string]any{"name": "Docs"}, &lic)
	lid := lic["id"]
	if st := uploadDoc(t, admin, lid, "Rechnung 2026-0142.pdf", []byte("%PDF invoice v1"), false); st != 201 {
		t.Fatalf("upload: %d", st)
	}
	uploadDoc(t, admin, lid, "EULA Brand Sans.pdf", []byte("%PDF eula"), true)
	docs := func(c *client) []licenseDoc {
		var r struct {
			License licenseRow `json:"license"`
		}
		c.do("GET", "/api/licenses/"+itoa(lid), nil, &r)
		return r.License.Documents
	}
	d := docs(admin)
	if len(d) != 2 || d[0].Kind != "eula" || d[1].Kind != "invoice" || d[1].UploadedBy == "" {
		t.Fatalf("documents: %+v", d)
	}
	eula, inv := d[0], d[1]

	// update: type, name; bad values refused
	if st := admin.do("PATCH", "/api/licenses/"+itoa(lid)+"/documents/"+itoa(inv.ID), map[string]any{"kind": "receipt"}, nil); st != 400 {
		t.Fatalf("bad kind: %d", st)
	}
	if st := admin.do("PATCH", "/api/licenses/"+itoa(lid)+"/documents/"+itoa(inv.ID), map[string]any{"filename": "  "}, nil); st != 400 {
		t.Fatalf("empty name: %d", st)
	}
	admin.do("PATCH", "/api/licenses/"+itoa(lid)+"/documents/"+itoa(eula.ID), map[string]any{"kind": "certificate", "filename": "Certificate.pdf"}, nil)
	if st := admin.do("PATCH", "/api/licenses/"+itoa(lid)+"/documents/999", map[string]any{"kind": "eula"}, nil); st != 404 {
		t.Fatalf("missing doc: %d", st)
	}

	// replace: new content and name, old file gone from the store
	var oldSha string
	s.db.QueryRow(`SELECT sha256 FROM license_documents WHERE id = ?`, inv.ID).Scan(&oldSha)
	if st := replaceDoc(t, admin, lid, inv.ID, "Rechnung 2027-0007.pdf", []byte("%PDF invoice v2")); st != 200 {
		t.Fatalf("replace: %d", st)
	}
	_, _, body := admin.get("/api/licenses/" + itoa(lid) + "/documents/" + itoa(inv.ID) + "?download=1")
	if body != "%PDF invoice v2" {
		t.Fatalf("replaced content: %q", body)
	}
	if _, err := os.Stat(s.blobPath(oldSha)); err == nil {
		t.Fatal("old file must be removed from the store")
	}
	d = docs(admin)
	var gotInv licenseDoc
	for _, x := range d {
		if x.ID == inv.ID {
			gotInv = x
		}
	}
	if gotInv.Filename != "Rechnung 2027-0007.pdf" || gotInv.Kind != "invoice" || gotInv.Shared || gotInv.UpdatedAt == 0 {
		t.Fatalf("after replace: %+v", gotInv)
	}

	// users: only shared documents, no uploader names, no changes
	admin.do("POST", "/api/users", map[string]any{"username": "gil", "password": "gilpassword1"}, nil)
	gil := newClient(t, ts.URL)
	gil.do("POST", "/api/auth/login", map[string]string{"username": "gil", "password": "gilpassword1"}, nil)
	if st := replaceDoc(t, gil, lid, inv.ID, "x.pdf", []byte("x")); st != 403 {
		t.Fatalf("user replace: %d", st)
	}
	if st, _, _ := gil.get("/api/licenses/" + itoa(lid) + "/documents/" + itoa(inv.ID)); st != 404 {
		t.Fatalf("user reads admin-only doc: %d", st)
	}

	// delete
	if st := admin.do("DELETE", "/api/licenses/"+itoa(lid)+"/documents/"+itoa(inv.ID), nil, nil); st != 200 {
		t.Fatalf("delete: %d", st)
	}
	if d = docs(admin); len(d) != 1 || d[0].Filename != "Certificate.pdf" || d[0].Kind != "certificate" {
		t.Fatalf("after delete: %+v", d)
	}
}
