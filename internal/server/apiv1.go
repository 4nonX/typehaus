package server

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ---- API v1 ---------------------------------------------------------------
//
// /api/v1 is the stable interface for other tools. It maps a fixed set of
// endpoints onto the same handlers the web UI uses; the set and the shapes
// of its requests and responses only change compatibly within v1 (new
// fields and endpoints may appear). The OpenAPI description at
// /api/v1/openapi.json is generated from this table and the Go types, so it
// can't drift from what the server does.
//
// Callers authenticate with an API token (Authorization: Bearer thk_…) or,
// from the browser, with a session. A token acts as an administrator-level
// service: "read" tokens may only GET, "write" tokens may also change data.
// Tokens never work outside /api/v1 (sign-in, accounts, agents, tokens).

type apiRoute struct {
	Method, Path string // path below /api/v1
	Admin        bool   // sessions: administrators only
	H            func(http.ResponseWriter, *http.Request, *authUser)
	Tag, Summary string
	Description  string
	Query        []apiParam
	Body         any    // JSON request body type
	Multipart    string // multipart/form-data upload: description of the parts
	Resp         any    // JSON response type (nil: {"ok": true})
	Status       int    // success status, default 200
	Produces     string // non-JSON response: text/csv, application/octet-stream, application/zip
	// Paged: the response (or its PageField) is a list served in pages with
	// limit/offset, filterable by the entries' simple fields.
	Paged     bool
	PageField string
}

type apiParam struct {
	Name, Type, Description string
}

// documentation-only request and response shapes for handlers that read or
// write small ad-hoc objects
type (
	apiFontIDs struct {
		FontIDs []int64 `json:"font_ids"`
	}
	apiAssignLicense struct {
		FontIDs   []int64 `json:"font_ids"`
		LicenseID *int64  `json:"license_id"` // null or 0: no license
	}
	apiCreated struct {
		ID int64 `json:"id"`
	}
	apiOK struct {
		OK bool `json:"ok"`
	}
	apiUploadResponse struct {
		Results []uploadResult `json:"results"`
		FontIDs []int64        `json:"font_ids"`
	}
	apiDocsAdded struct {
		Added []string `json:"added"`
	}
	apiDocReplaced struct {
		Filename string `json:"filename"`
	}
	apiDocUpdate struct {
		Filename *string `json:"filename"`
		Kind     *string `json:"kind"` // eula | invoice | certificate | other
		Shared   *bool   `json:"shared"`
	}
	apiRequestAnswer struct {
		Status *string `json:"status"` // open | done | declined
		Answer *string `json:"answer"`
	}
)

func (s *Server) v1Routes() []apiRoute {
	return []apiRoute{
		// fonts
		{Method: "GET", Paged: true, Path: "/fonts", H: s.handleListFonts, Tag: "Fonts", Summary: "List fonts",
			Description: "Every style in the library (tokens and admins see all). activated/required describe the signed-in user and are false for tokens.",
			Query: []apiParam{{"q", "string", "search family, name, PostScript name, designer and foundry"},
				{"collection", "integer", "only fonts in this collection"}}, Resp: []fontRow{}},
		{Method: "GET", Path: "/fonts/{id}", H: s.handleGetFont, Tag: "Fonts", Summary: "Get a font",
			Description: "The font with what its file says about foundry, copyright and license.", Resp: fontDetail{}},
		{Method: "GET", Path: "/fonts/download", H: s.handleDownloadFonts, Tag: "Fonts", Summary: "Download font files",
			Description: "One font file, or a ZIP for several (with Type 1 metrics and license notices). License rules apply to tokens without exception: fonts whose license forbids downloading, or that is enforced with limited seats or expired, are left out and named in the X-Typehaus-Skipped header. Honours TYPEHAUS_DOWNLOADS.",
			Query:       []apiParam{{"ids", "string", "comma-separated font ids"}}, Produces: "application/octet-stream"},
		{Method: "POST", Path: "/fonts", Admin: true, H: s.handleUpload, Tag: "Fonts", Summary: "Upload fonts",
			Multipart: "files: one or more font files or ZIP archives (any supported format); collection_id (optional): add the new fonts to this collection; force (optional): 1 adds a file even when the same font is already in the library in another file",
			Resp:      apiUploadResponse{}},
		{Method: "PATCH", Path: "/fonts", Admin: true, H: s.handleBulkUpdateFonts, Tag: "Fonts", Summary: "Assign a license to fonts",
			Body: apiAssignLicense{}},
		{Method: "DELETE", Path: "/fonts", Admin: true, H: s.handleDeleteFonts, Tag: "Fonts", Summary: "Delete fonts",
			Description: "Removes the fonts from the library; computers remove them at their next sync.", Body: apiFontIDs{}},

		// collections
		{Method: "GET", Paged: true, Path: "/collections", H: s.handleListCollections, Tag: "Collections", Summary: "List collections", Resp: []collectionRow{}},
		{Method: "POST", Path: "/collections", Admin: true, H: s.handleCreateCollection, Tag: "Collections", Summary: "Create a collection",
			Body: collectionReq{}, Resp: apiCreated{}, Status: 201},
		{Method: "PATCH", Path: "/collections/{id}", Admin: true, H: s.handleUpdateCollection, Tag: "Collections", Summary: "Change a collection",
			Description: "mode: available (people switch fonts on) or installed (installed automatically for everyone it applies to). Refused with 409 when an enforced license would run out of seats.",
			Body:        collectionReq{}},
		{Method: "DELETE", Path: "/collections/{id}", Admin: true, H: s.handleDeleteCollection, Tag: "Collections", Summary: "Delete a collection"},
		{Method: "POST", Path: "/collections/{id}/fonts", Admin: true, H: s.handleCollectionAddFonts, Tag: "Collections", Summary: "Add fonts to a collection", Body: apiFontIDs{}},
		{Method: "DELETE", Path: "/collections/{id}/fonts", Admin: true, H: s.handleCollectionRemoveFonts, Tag: "Collections", Summary: "Remove fonts from a collection", Body: apiFontIDs{}},

		// licenses
		{Method: "GET", Paged: true, Path: "/licenses", H: s.handleListLicenses, Tag: "Licenses", Summary: "List licenses", Resp: []licenseRow{}},
		{Method: "GET", Path: "/licenses/{id}", H: s.handleGetLicense, Tag: "Licenses", Summary: "Get a license",
			Description: "With its families and (admins and tokens) the people or computers holding a seat.", Resp: licenseDetail{}},
		{Method: "POST", Path: "/licenses", Admin: true, H: s.handleCreateLicense, Tag: "Licenses", Summary: "Create a license",
			Body: licenseReq{}, Resp: apiCreated{}, Status: 201},
		{Method: "PATCH", Path: "/licenses/{id}", Admin: true, H: s.handleUpdateLicense, Tag: "Licenses", Summary: "Change a license", Body: licenseReq{}},
		{Method: "DELETE", Path: "/licenses/{id}", Admin: true, H: s.handleDeleteLicense, Tag: "Licenses", Summary: "Delete a license",
			Description: "Its documents are deleted; its fonts stay in the library without a license."},
		{Method: "GET", Paged: true, Path: "/licenses/{id}/history", Admin: true, H: s.handleLicenseHistory, Tag: "Licenses", Summary: "Daily usage of a license",
			Query: []apiParam{{"days", "integer", "how far back, default 365"}}, Resp: []usagePoint{}},
		{Method: "GET", Path: "/licenses/export.csv", Admin: true, H: s.handleLicenseReport, Tag: "Licenses", Summary: "License report (CSV)", Produces: "text/csv"},
		{Method: "GET", Path: "/licenses/history.csv", Admin: true, H: s.handleUsageCSV, Tag: "Licenses", Summary: "Daily usage of all licenses (CSV)", Produces: "text/csv"},
		{Method: "POST", Path: "/licenses/{id}/documents", Admin: true, H: s.handleUploadLicenseDocs, Tag: "License documents", Summary: "Attach documents",
			Multipart: "files: one or more files (50 MB each); shared: 1 or 0 (visible to users, default 1); kind (optional): eula, invoice, certificate or other – otherwise read from each file name",
			Resp:      apiDocsAdded{}, Status: 201},
		{Method: "GET", Path: "/licenses/{id}/documents/{doc}", H: s.handleLicenseDoc, Tag: "License documents", Summary: "Get a document's file",
			Query: []apiParam{{"download", "integer", "1: as attachment"}}, Produces: "application/octet-stream"},
		{Method: "PATCH", Path: "/licenses/{id}/documents/{doc}", Admin: true, H: s.handleUpdateLicenseDoc, Tag: "License documents", Summary: "Rename a document, change its type or visibility", Body: apiDocUpdate{}},
		{Method: "PUT", Path: "/licenses/{id}/documents/{doc}", Admin: true, H: s.handleReplaceLicenseDoc, Tag: "License documents", Summary: "Replace a document's file",
			Description: "Type and visibility stay; the name becomes the new file's.", Multipart: "file: the new file (50 MB)", Resp: apiDocReplaced{}},
		{Method: "DELETE", Path: "/licenses/{id}/documents/{doc}", Admin: true, H: s.handleDeleteLicenseDoc, Tag: "License documents", Summary: "Delete a document"},

		// requests
		{Method: "GET", Paged: true, Path: "/requests", H: s.handleListRequests, Tag: "Font requests", Summary: "List font requests",
			Description: "Admins and tokens: everyone's requests.", Resp: []fontRequest{}},
		{Method: "PATCH", Path: "/requests/{id}", Admin: true, H: s.handleUpdateRequest, Tag: "Font requests", Summary: "Answer a font request", Body: apiRequestAnswer{}},

		// people
		{Method: "GET", Paged: true, Path: "/users", Admin: true, H: s.handleListUsers, Tag: "People", Summary: "List users", Resp: []userRow{}},
		{Method: "POST", Path: "/users", Admin: true, H: s.handleCreateUser, Tag: "People", Summary: "Create a user", Body: userReq{}, Resp: apiCreated{}, Status: 201},
		{Method: "PATCH", Path: "/users/{id}", Admin: true, H: s.handleUpdateUser, Tag: "People", Summary: "Change a user",
			Description: "disabled: true removes the fonts from the user's computers until enabled again.", Body: userReq{}},
		{Method: "DELETE", Path: "/users/{id}", Admin: true, H: s.handleDeleteUser, Tag: "People", Summary: "Delete a user"},
		{Method: "GET", Paged: true, Path: "/groups", Admin: true, H: s.handleListGroups, Tag: "People", Summary: "List groups", Resp: []groupRow{}},
		{Method: "POST", Path: "/groups", Admin: true, H: s.handleCreateGroup, Tag: "People", Summary: "Create a group", Body: groupReq{}, Resp: apiCreated{}, Status: 201},
		{Method: "PATCH", Path: "/groups/{id}", Admin: true, H: s.handleUpdateGroup, Tag: "People", Summary: "Change a group", Body: groupReq{}},
		{Method: "DELETE", Path: "/groups/{id}", Admin: true, H: s.handleDeleteGroup, Tag: "People", Summary: "Delete a group"},

		// devices
		{Method: "GET", Paged: true, Path: "/devices", H: s.handleListDevices, Tag: "Devices", Summary: "List computers",
			Query: []apiParam{{"all", "integer", "1: every computer (admins and tokens); otherwise only the signed-in user's"}}, Resp: []deviceRow{}},
		{Method: "DELETE", Path: "/devices/{id}", H: s.handleDeleteDevice, Tag: "Devices", Summary: "Disconnect a computer",
			Description: "The computer removes its typehaus fonts and forgets the connection."},

		// compliance
		{Method: "GET", Paged: true, PageField: "findings", Path: "/inventory", Admin: true, H: s.handleInventory, Tag: "Compliance", Summary: "Copies of library fonts found outside typehaus", Resp: inventoryResult{}},
		{Method: "GET", Path: "/inventory.csv", Admin: true, H: s.handleInventoryCSV, Tag: "Compliance", Summary: "Copies found outside typehaus (CSV)", Produces: "text/csv"},
		{Method: "GET", Paged: true, Path: "/reports/usage", Admin: true, H: s.handleUsageReport, Tag: "Compliance", Summary: "Usage by family", Resp: []usageRow{}},
		// free font catalog and identification
		{Paged: true, Method: "GET", Path: "/catalog", H: s.handleCatalogSearch, Tag: "Catalog", Summary: "Search the free font catalog",
			Description: "Families from Google Fonts, Font Squirrel and Fontsource. Google Fonts and Font Squirrel families can be imported; other Fontsource families are listed with the reason they can't. Refreshed daily; 404 feature_disabled when TYPEHAUS_CATALOG is off, 502 upstream_error when no catalog could be reached.",
			Query: []apiParam{{"q", "string", "words in the family or foundry name"},
				{"category", "string", "sans-serif, serif, display, handwriting or monospace"},
				{"source", "string", "google, fontsquirrel or fontsource"},
				{"license", "string", "SPDX id (e.g. OFL-1.1) or Freeware"}}, Resp: []catalogFamily{}},
		{Method: "POST", Path: "/catalog/import", Admin: true, H: s.handleCatalogImport, Tag: "Catalog", Summary: "Import catalog families into the library",
			Description: "Downloads the complete families (Google Fonts from Google's repository, Font Squirrel's desktop package) and adds them like an upload. Open licenses are assigned automatically; a Font Squirrel freeware license becomes a license record with the license text attached, for review. Each family reports added, exists or error.",
			Body:        catalogImportReq{}, Resp: []catalogImportResult{}},
		{Method: "GET", Path: "/lookup", H: s.handleLookup, Tag: "Catalog", Summary: "Look a font name up in the library and the catalog",
			Description: "For a name from an identification site: the library families it matches (visible to the caller) and catalog families.",
			Query:       []apiParam{{"name", "string", "a font or family name, e.g. Helvetica Neue LT Pro 55 Roman"}}, Resp: lookupResponse{}},
		{Method: "POST", Path: "/identify", H: s.handleIdentify, Tag: "Catalog", Summary: "Identify the font in an image",
			Description: "Sends the image to every identification service with an API key (WhatFontIs, Mixfont Lens, Aspose.OCR Cloud) and matches the results against the library and the catalog. 409 not_configured when no service has a key; 30 identifications an hour per person.",
			Multipart:   "image: PNG, JPEG, GIF, WebP or BMP up to 10 MB, or url: a link to one; services (optional): comma-separated service ids; free_only (optional): 1 asks for free fonts only (WhatFontIs)",
			Resp:        identifyResponse{}},

		// web fonts
		{Paged: true, Method: "GET", Path: "/webkits", Admin: true, H: s.handleListWebkits, Tag: "Web fonts", Summary: "List web kits",
			Description: "Each kit with its fonts (served, or the problem that keeps one out of the stylesheet) and this month's page views.", Resp: []webkitRow{}},
		{Method: "POST", Path: "/webkits", Admin: true, H: s.handleCreateWebkit, Tag: "Web fonts", Summary: "Create a web kit",
			Description: "Websites embed css_url as a stylesheet. Only fonts whose license includes web use and hasn't expired are served, only to the domains listed (www.example.com; *.example.com for subdomains; * for any site). Every stylesheet request from a listed domain counts as a page view against the licenses' monthly limit (web_pageviews).",
			Body:        webkitReq{}, Resp: webkitCreated{}, Status: 201},
		{Method: "GET", Path: "/webkits/{id}", Admin: true, H: s.handleGetWebkit, Tag: "Web fonts", Summary: "Get a web kit", Resp: webkitRow{}},
		{Method: "PATCH", Path: "/webkits/{id}", Admin: true, H: s.handleUpdateWebkit, Tag: "Web fonts", Summary: "Change a web kit",
			Description: "font_ids replaces the kit's fonts. enabled: false stops serving the kit (its stylesheet answers 404).", Body: webkitReq{}},
		{Method: "DELETE", Path: "/webkits/{id}", Admin: true, H: s.handleDeleteWebkit, Tag: "Web fonts", Summary: "Delete a web kit"},

		// webhooks
		{Method: "GET", Path: "/webhook-events", Admin: true, H: s.handleWebhookEvents, Tag: "Webhooks", Summary: "Event types webhooks can subscribe to", Resp: []webhookEventInfo{}},
		{Paged: true, Method: "GET", Path: "/webhooks", Admin: true, H: s.handleListWebhooks, Tag: "Webhooks", Summary: "List webhooks", Resp: []webhookRow{}},
		{Method: "POST", Path: "/webhooks", Admin: true, H: s.handleCreateWebhook, Tag: "Webhooks", Summary: "Create a webhook",
			Description: "events: event types, a family like \"license.*\", or [\"*\"] for all (default). The response holds the signing secret – it is shown only here.",
			Body:        webhookReq{}, Resp: webhookCreated{}, Status: 201},
		{Method: "GET", Path: "/webhooks/{id}", Admin: true, H: s.handleGetWebhook, Tag: "Webhooks", Summary: "Get a webhook", Resp: webhookRow{}},
		{Method: "PATCH", Path: "/webhooks/{id}", Admin: true, H: s.handleUpdateWebhook, Tag: "Webhooks", Summary: "Change a webhook", Body: webhookReq{}},
		{Method: "DELETE", Path: "/webhooks/{id}", Admin: true, H: s.handleDeleteWebhook, Tag: "Webhooks", Summary: "Delete a webhook"},
		{Method: "POST", Path: "/webhooks/{id}/secret", Admin: true, H: s.handleRotateWebhookSecret, Tag: "Webhooks", Summary: "New signing secret",
			Description: "The old secret stops working at once.", Resp: webhookCreated{}},
		{Method: "POST", Path: "/webhooks/{id}/test", Admin: true, H: s.handleTestWebhook, Tag: "Webhooks", Summary: "Send a webhook.test event", Resp: apiCreated{}, Status: 202},
		{Paged: true, Method: "GET", Path: "/webhooks/{id}/deliveries", Admin: true, H: s.handleListDeliveries, Tag: "Webhooks", Summary: "Deliveries, newest first (30 days)", Resp: []webhookDelivery{}},
		{Method: "POST", Path: "/webhooks/{id}/deliveries/{delivery}/redeliver", Admin: true, H: s.handleRedeliver, Tag: "Webhooks", Summary: "Send a delivery again", Status: 202},

		{Method: "GET", Path: "/audit", Admin: true, H: s.handleAudit, Tag: "Compliance", Summary: "Activity log",
			Query: []apiParam{{"limit", "integer", "max. entries, default 200, at most 1000"}, {"before", "integer", "entries older than this id (paging)"}},
			Resp:  []auditEntry{}},
	}
}

func (s *Server) registerV1(mux *http.ServeMux) {
	for _, rt := range s.v1Routes() {
		mux.Handle(rt.Method+" /api/v1"+rt.Path, s.v1(rt))
	}
	mux.HandleFunc("GET /api/v1/openapi.json", s.handleOpenAPI)
}

// v1 authenticates an API request: a token, or else the browser session.
func (s *Server) v1(rt apiRoute) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if bearer(r) != "" {
			u, err := s.tokenUser(r)
			if err != nil {
				w.Header().Set("WWW-Authenticate", `Bearer realm="typehaus"`)
				code := "token_invalid"
				var te *tokenError
				if errors.As(err, &te) {
					code = te.code
				}
				writeErrCode(w, http.StatusUnauthorized, code, err.Error())
				return
			}
			if !s.rateAllow(w, u.Token.ID) {
				return
			}
			// lifecycle hints for the integration's monitoring
			if u.Token.ExpiresAt != nil {
				w.Header().Set("X-Typehaus-Token-Expires", time.Unix(*u.Token.ExpiresAt, 0).UTC().Format(time.RFC3339))
			}
			if u.Token.usedPrevious {
				w.Header().Set("X-Typehaus-Token-Superseded", time.Unix(*u.Token.PrevUntil, 0).UTC().Format(time.RFC3339))
			}
			if r.Method != http.MethodGet && r.Method != http.MethodHead && u.Token.Scope != "write" {
				writeErrCode(w, http.StatusForbidden, "read_only_token", "this token may only read")
				return
			}
			s.idempotent(w, r, "t"+itoa64(u.Token.ID), func(w http.ResponseWriter) { s.serveV1(w, r, u, rt) })
			return
		}
		u := s.sessionUser(r)
		if u == nil {
			writeErr(w, http.StatusUnauthorized, "not logged in – send an API token as Authorization: Bearer …")
			return
		}
		if rt.Admin && !u.IsAdmin {
			writeErrCode(w, http.StatusForbidden, "admin_only", "admin only")
			return
		}
		s.idempotent(w, r, "u"+itoa64(u.ID), func(w http.ResponseWriter) { s.serveV1(w, r, u, rt) })
	})
}

func itoa64(i int64) string { return strconv.FormatInt(i, 10) }

// ---- idempotency ------------------------------------------------------------

// teeWriter sends a response and keeps a copy of it.
type teeWriter struct {
	http.ResponseWriter
	status int
	buf    bytes.Buffer
}

func (t *teeWriter) WriteHeader(code int) { t.status = code; t.ResponseWriter.WriteHeader(code) }
func (t *teeWriter) Write(p []byte) (int, error) {
	if t.status == 0 {
		t.status = 200
	}
	if t.buf.Len() < 1<<20 {
		t.buf.Write(p)
	}
	return t.ResponseWriter.Write(p)
}

// idempotent runs a write request once per Idempotency-Key: a retry with
// the same key gets the stored response instead of running it again.
func (s *Server) idempotent(w http.ResponseWriter, r *http.Request, principal string, run func(http.ResponseWriter)) {
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if key == "" || r.Method == http.MethodGet || r.Method == http.MethodHead {
		run(w)
		return
	}
	if len(key) > 255 {
		writeErr(w, 400, "Idempotency-Key is longer than 255 characters")
		return
	}
	// the request body decides whether a retry is the same request; uploads
	// can be large, so it goes through a temporary file
	tmpDir := filepath.Join(s.cfg.DataDir, "tmp")
	os.MkdirAll(tmpDir, 0o700)
	f, err := os.CreateTemp(tmpDir, "idem-")
	if err != nil {
		s.internalErr(w, err)
		return
	}
	defer func() { f.Close(); os.Remove(f.Name()) }()
	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(f, h), http.MaxBytesReader(w, r.Body, s.cfg.MaxUploadMB<<20)); err != nil {
		writeErrCode(w, 413, "payload_too_large", "request body too large or interrupted")
		return
	}
	f.Seek(0, io.SeekStart)
	r.Body = io.NopCloser(f)
	bodyHash := hex.EncodeToString(h.Sum([]byte(r.Header.Get("Content-Type"))))

	res, err := s.db.Exec(`INSERT OR IGNORE INTO api_idempotency (principal, key, method, path, body_hash, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		principal, key, r.Method, r.URL.Path, bodyHash, now())
	if err != nil {
		s.internalErr(w, err)
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		var method, path, hash, ctype string
		var status int
		var body []byte
		s.db.QueryRow(`SELECT method, path, body_hash, status, content_type, response FROM api_idempotency WHERE principal = ? AND key = ?`,
			principal, key).Scan(&method, &path, &hash, &status, &ctype, &body)
		switch {
		case method != r.Method || path != r.URL.Path || hash != bodyHash:
			writeErrCode(w, 422, "idempotency_key_reused", "this Idempotency-Key was already used for a different request")
		case status == 0:
			writeErrCode(w, 409, "idempotency_in_progress", "a request with this Idempotency-Key is still being processed")
		default:
			w.Header().Set("Content-Type", ctype)
			w.Header().Set("Idempotent-Replayed", "true")
			w.WriteHeader(status)
			w.Write(body)
		}
		return
	}
	tw := &teeWriter{ResponseWriter: w}
	run(tw)
	if tw.status == 0 {
		tw.status = 200
	}
	if tw.status >= 500 {
		// not a result: let the retry run
		s.db.Exec(`DELETE FROM api_idempotency WHERE principal = ? AND key = ?`, principal, key)
		return
	}
	s.db.Exec(`UPDATE api_idempotency SET status = ?, content_type = ?, response = ? WHERE principal = ? AND key = ?`,
		tw.status, w.Header().Get("Content-Type"), tw.buf.Bytes(), principal, key)
}

// ---- pagination and filtering --------------------------------------------------

const (
	pageDefault = 100
	pageMax     = 1000
)

// bufWriter keeps a handler's response so it can be paged before sending.
type bufWriter struct {
	h      http.Header
	status int
	buf    bytes.Buffer
}

func (b *bufWriter) Header() http.Header         { return b.h }
func (b *bufWriter) WriteHeader(code int)        { b.status = code }
func (b *bufWriter) Write(p []byte) (int, error) { return b.buf.Write(p) }

func (s *Server) serveV1(w http.ResponseWriter, r *http.Request, u *authUser, rt apiRoute) {
	if !rt.Paged {
		rt.H(w, r, u)
		return
	}
	q := r.URL.Query()
	limit, offset := pageDefault, 0
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > pageMax {
			writeErr(w, 400, "limit must be between 1 and "+itoaInt(pageMax))
			return
		}
		limit = n
	}
	if v := q.Get("offset"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 {
			writeErr(w, 400, "offset must be 0 or more")
			return
		}
		offset = n
	}
	filters, err := listFilters(q, rt)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	bw := &bufWriter{h: w.Header(), status: 200}
	rt.H(bw, r, u)
	if bw.status != 200 {
		w.WriteHeader(bw.status)
		w.Write(bw.buf.Bytes())
		return
	}
	var root any
	dec := json.NewDecoder(&bw.buf)
	dec.UseNumber()
	if err := dec.Decode(&root); err != nil {
		s.internalErr(w, err)
		return
	}
	list, _ := root.([]any)
	if rt.PageField != "" {
		obj, _ := root.(map[string]any)
		list, _ = obj[rt.PageField].([]any)
	}
	kept := list[:0:0]
	for _, item := range list {
		if matchFilters(item, filters) {
			kept = append(kept, item)
		}
	}
	total := len(kept)
	page := []any{}
	if offset < total {
		page = kept[offset:min(total, offset+limit)]
	}
	var out any = page
	if rt.PageField != "" {
		obj := root.(map[string]any)
		obj[rt.PageField] = page
		out = obj
	}
	h := w.Header()
	h.Set("X-Total-Count", itoaInt(total))
	var links []string
	link := func(off int, rel string) {
		v := r.URL.Query()
		v.Set("limit", itoaInt(limit))
		v.Set("offset", itoaInt(off))
		links = append(links, "<"+r.URL.Path+"?"+v.Encode()+">; rel=\""+rel+"\"")
	}
	if offset+limit < total {
		link(offset+limit, "next")
	}
	if offset > 0 {
		link(max(0, offset-limit), "prev")
	}
	if len(links) > 0 {
		h.Set("Link", strings.Join(links, ", "))
	}
	writeJSON(w, 200, out)
}

// filterFields are the simple fields of a list's entries: each works as an
// exact-match query filter (comma-separated values: any of them).
func filterFields(rt apiRoute) map[string]reflect.Kind {
	t := reflect.TypeOf(rt.Resp)
	if t == nil {
		return nil
	}
	if rt.PageField != "" {
		for t.Kind() == reflect.Pointer {
			t = t.Elem()
		}
		for i := 0; i < t.NumField(); i++ {
			if strings.Split(t.Field(i).Tag.Get("json"), ",")[0] == rt.PageField {
				t = t.Field(i).Type
				break
			}
		}
	}
	if t.Kind() != reflect.Slice {
		return nil
	}
	t = t.Elem()
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	out := map[string]reflect.Kind{}
	reserved := map[string]bool{"limit": true, "offset": true}
	for _, p := range rt.Query {
		reserved[p.Name] = true
	}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		name := strings.Split(f.Tag.Get("json"), ",")[0]
		if name == "" || name == "-" || reserved[name] || !f.IsExported() {
			continue
		}
		ft := f.Type
		for ft.Kind() == reflect.Pointer {
			ft = ft.Elem()
		}
		switch ft.Kind() {
		case reflect.String, reflect.Bool, reflect.Int, reflect.Int64, reflect.Int32:
			out[name] = ft.Kind()
		}
	}
	return out
}

func listFilters(q url.Values, rt apiRoute) (map[string][]string, error) {
	fields := filterFields(rt)
	out := map[string][]string{}
	for k, vs := range q {
		kind, ok := fields[k]
		if !ok {
			continue // not a filter (or a parameter of the endpoint itself)
		}
		for _, v := range strings.Split(strings.Join(vs, ","), ",") {
			v = strings.TrimSpace(v)
			switch kind {
			case reflect.Bool:
				switch strings.ToLower(v) {
				case "true", "1":
					v = "true"
				case "false", "0":
					v = "false"
				default:
					return nil, errors.New(k + " must be true or false")
				}
			case reflect.Int, reflect.Int64, reflect.Int32:
				if _, err := strconv.ParseInt(v, 10, 64); err != nil {
					return nil, errors.New(k + " must be a number")
				}
			}
			out[k] = append(out[k], v)
		}
	}
	return out, nil
}

func matchFilters(item any, filters map[string][]string) bool {
	obj, ok := item.(map[string]any)
	if !ok {
		return len(filters) == 0
	}
	for k, want := range filters {
		var got string
		switch v := obj[k].(type) {
		case nil:
			got = ""
		case string:
			got = v
		case bool:
			got = strconv.FormatBool(v)
		case json.Number:
			got = v.String()
		default:
			return false
		}
		hit := false
		for _, w := range want {
			hit = hit || strings.EqualFold(got, w)
		}
		if !hit {
			return false
		}
	}
	return true
}

// ---- tokens ----------------------------------------------------------------

const tokenPrefix = "thk_"

type apiToken struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Prefix    string `json:"prefix"` // first characters, to recognise it
	Scope     string `json:"scope"`  // read | write
	CreatedBy string `json:"created_by"`
	CreatedAt int64  `json:"created_at"`
	ExpiresAt *int64 `json:"expires_at"`
	LastUsed  *int64 `json:"last_used_at"`
	LastIP    string `json:"last_ip"`
	RotatedAt *int64 `json:"rotated_at"`
	PrevUntil *int64 `json:"previous_valid_until"` // the secret before the last rotation works until then
	// set on the principal: the request used the previous secret
	usedPrevious bool
}

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if len(h) > 7 && strings.EqualFold(h[:7], "bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return ""
}

var tokenUse sync.Map // token id -> last recorded use (unix)

type tokenError struct{ code, msg string }

func (e *tokenError) Error() string { return e.msg }

// tokenUser resolves an API token to an administrator-level principal. It
// stops working when it expires, is revoked, or its creator is disabled or
// no longer an administrator.
func (s *Server) tokenUser(r *http.Request) (*authUser, error) {
	tok := bearer(r)
	if !strings.HasPrefix(tok, tokenPrefix) {
		return nil, &tokenError{"token_invalid", "not a typehaus API token"}
	}
	var t apiToken
	var uid int64
	var exp, prevUntil sql.NullInt64
	var admin, disabled bool
	h := hashToken(tok)
	err := s.db.QueryRow(`SELECT t.id, t.name, t.scope, t.expires_at, t.prev_until, t.token_hash != ?, u.id, u.is_admin, u.disabled FROM api_tokens t
		JOIN users u ON u.id = t.created_by WHERE t.token_hash = ? OR t.prev_hash = ?`, h, h, h).
		Scan(&t.ID, &t.Name, &t.Scope, &exp, &prevUntil, &t.usedPrevious, &uid, &admin, &disabled)
	if err != nil {
		return nil, &tokenError{"token_invalid", "unknown or revoked API token"}
	}
	if t.usedPrevious && (!prevUntil.Valid || prevUntil.Int64 < now()) {
		return nil, &tokenError{"token_invalid", "this secret was replaced when the token was rotated"}
	}
	if exp.Valid && exp.Int64 < now() {
		return nil, &tokenError{"token_expired", "this API token has expired"}
	}
	if exp.Valid {
		t.ExpiresAt = &exp.Int64
	}
	if prevUntil.Valid {
		t.PrevUntil = &prevUntil.Int64
	}
	if !admin || disabled {
		return nil, &tokenError{"token_owner_inactive", "the administrator who created this token is no longer active"}
	}
	n := now()
	if last, ok := tokenUse.Load(t.ID); !ok || n-last.(int64) >= 60 {
		tokenUse.Store(t.ID, n)
		s.db.Exec(`UPDATE api_tokens SET last_used_at = ?, last_ip = ? WHERE id = ?`, n, s.clientIP(r), t.ID)
	}
	return &authUser{ID: uid, Username: "token:" + t.Name, DisplayName: t.Name, IsAdmin: true, Token: &t, IP: s.clientIP(r)}, nil
}

func (s *Server) handleListTokens(w http.ResponseWriter, r *http.Request, u *authUser) {
	rows, err := s.db.Query(`SELECT t.id, t.name, t.prefix, t.scope, COALESCE(NULLIF(u.display_name, ''), u.username),
		t.created_at, t.expires_at, t.last_used_at, t.last_ip, t.rotated_at, t.prev_until
		FROM api_tokens t JOIN users u ON u.id = t.created_by ORDER BY t.created_at DESC`)
	if err != nil {
		s.internalErr(w, err)
		return
	}
	defer rows.Close()
	out := []apiToken{}
	for rows.Next() {
		var t apiToken
		var exp, used, rot, prev sql.NullInt64
		rows.Scan(&t.ID, &t.Name, &t.Prefix, &t.Scope, &t.CreatedBy, &t.CreatedAt, &exp, &used, &t.LastIP, &rot, &prev)
		if rot.Valid {
			t.RotatedAt = &rot.Int64
		}
		if prev.Valid && prev.Int64 > now() {
			t.PrevUntil = &prev.Int64
		}
		if exp.Valid {
			t.ExpiresAt = &exp.Int64
		}
		if used.Valid {
			t.LastUsed = &used.Int64
		}
		out = append(out, t)
	}
	writeJSON(w, 200, out)
}

func (s *Server) handleCreateToken(w http.ResponseWriter, r *http.Request, u *authUser) {
	var req struct {
		Name    string `json:"name"`
		Scope   string `json:"scope"`
		Expires int    `json:"expires_days"` // 0 = never
	}
	if !readJSON(w, r, &req) {
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" || len(req.Name) > 100 {
		writeErr(w, 400, "name required (max. 100 characters)")
		return
	}
	if req.Scope != "read" && req.Scope != "write" {
		writeErr(w, 400, "scope must be read or write")
		return
	}
	if req.Expires < 0 || req.Expires > 3650 {
		writeErr(w, 400, "expires_days must be between 0 (never) and 3650")
		return
	}
	var exp any
	if req.Expires > 0 {
		exp = time.Now().Add(time.Duration(req.Expires) * 24 * time.Hour).Unix()
	}
	tok := tokenPrefix + randomToken(32)
	res, err := s.db.Exec(`INSERT INTO api_tokens (name, token_hash, prefix, scope, created_by, created_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)`, req.Name, hashToken(tok), tok[:len(tokenPrefix)+6], req.Scope, u.ID, now(), exp)
	if err != nil {
		s.internalErr(w, err)
		return
	}
	id, _ := res.LastInsertId()
	s.audit(u, "token.create", "%s (%s)", req.Name, req.Scope)
	// the secret is shown this one time only
	writeJSON(w, 201, map[string]any{"id": id, "token": tok})
}

func (s *Server) handleDeleteToken(w http.ResponseWriter, r *http.Request, u *authUser) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, 400, "bad id")
		return
	}
	var name string
	if err := s.db.QueryRow(`SELECT name FROM api_tokens WHERE id = ?`, id).Scan(&name); err != nil {
		writeErr(w, 404, "token not found")
		return
	}
	s.db.Exec(`DELETE FROM api_tokens WHERE id = ?`, id)
	tokenUse.Delete(id)
	s.audit(u, "token.revoke", "%s", name)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// handleRotateToken gives a token a new secret. The previous one keeps
// working for grace_hours (0–168), so an integration can switch over
// without downtime.
func (s *Server) handleRotateToken(w http.ResponseWriter, r *http.Request, u *authUser) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, 400, "bad id")
		return
	}
	var req struct {
		Grace int `json:"grace_hours"`
	}
	if !readJSON(w, r, &req) {
		return
	}
	if req.Grace < 0 || req.Grace > 168 {
		writeErr(w, 400, "grace_hours must be between 0 and 168")
		return
	}
	var name string
	if err := s.db.QueryRow(`SELECT name FROM api_tokens WHERE id = ?`, id).Scan(&name); err != nil {
		writeErr(w, 404, "token not found")
		return
	}
	tok := tokenPrefix + randomToken(32)
	var until any
	if req.Grace > 0 {
		until = time.Now().Add(time.Duration(req.Grace) * time.Hour).Unix()
	}
	if _, err := s.db.Exec(`UPDATE api_tokens SET prev_hash = CASE WHEN ? IS NULL THEN NULL ELSE token_hash END, prev_until = ?,
		token_hash = ?, prefix = ?, rotated_at = ? WHERE id = ?`, until, until, hashToken(tok), tok[:len(tokenPrefix)+6], now(), id); err != nil {
		s.internalErr(w, err)
		return
	}
	s.audit(u, "token.rotate", "%s (previous secret valid %d h)", name, req.Grace)
	writeJSON(w, 200, map[string]any{"id": id, "token": tok})
}

// handleUpdateToken renames a token or changes its expiry.
func (s *Server) handleUpdateToken(w http.ResponseWriter, r *http.Request, u *authUser) {
	id, ok := pathID(r)
	if !ok {
		writeErr(w, 400, "bad id")
		return
	}
	var req struct {
		Name    *string `json:"name"`
		Expires *int    `json:"expires_days"` // from now; 0 = never
	}
	if !readJSON(w, r, &req) {
		return
	}
	if !s.exists("api_tokens", id) {
		writeErr(w, 404, "token not found")
		return
	}
	if req.Name != nil {
		n := strings.TrimSpace(*req.Name)
		if n == "" || len(n) > 100 {
			writeErr(w, 400, "name required (max. 100 characters)")
			return
		}
		s.db.Exec(`UPDATE api_tokens SET name = ? WHERE id = ?`, n, id)
	}
	if req.Expires != nil {
		if *req.Expires < 0 || *req.Expires > 3650 {
			writeErr(w, 400, "expires_days must be between 0 (never) and 3650")
			return
		}
		var exp any
		if *req.Expires > 0 {
			exp = time.Now().Add(time.Duration(*req.Expires) * 24 * time.Hour).Unix()
		}
		s.db.Exec(`UPDATE api_tokens SET expires_at = ? WHERE id = ?`, exp, id)
	}
	s.audit(u, "token.update", "token %d", id)
	writeJSON(w, 200, map[string]bool{"ok": true})
}

// ---- rate limiting (per token, token bucket) ---------------------------------

type bucket struct {
	tokens float64
	last   time.Time
}

var (
	rateMu      sync.Mutex
	rateBuckets = map[int64]*bucket{}
)

// rateAllow takes one request from the token's budget (APIRate per minute,
// bursts up to a sixth of that), sets the RateLimit headers and answers 429
// when it is used up.
func (s *Server) rateAllow(w http.ResponseWriter, tokenID int64) bool {
	limit := s.cfg.APIRate
	if limit <= 0 {
		return true
	}
	burst := float64(max(limit/6, 10))
	perSec := float64(limit) / 60
	rateMu.Lock()
	b := rateBuckets[tokenID]
	nowT := time.Now()
	if b == nil {
		b = &bucket{tokens: burst, last: nowT}
		rateBuckets[tokenID] = b
	}
	b.tokens = min(burst, b.tokens+nowT.Sub(b.last).Seconds()*perSec)
	b.last = nowT
	ok := b.tokens >= 1
	if ok {
		b.tokens--
	}
	remaining := int(b.tokens)
	wait := 0
	if !ok {
		wait = int((1-b.tokens)/perSec) + 1
	}
	rateMu.Unlock()
	h := w.Header()
	h.Set("RateLimit-Limit", itoaInt(limit))
	h.Set("RateLimit-Remaining", itoaInt(remaining))
	h.Set("RateLimit-Policy", itoaInt(limit)+";w=60")
	if !ok {
		h.Set("Retry-After", itoaInt(wait))
		writeErrCode(w, http.StatusTooManyRequests, "rate_limited", "too many requests for this token – retry after "+itoaInt(wait)+" s")
	}
	return ok
}

// noTokens keeps API tokens out of an endpoint (session only).
func (s *Server) noTokens(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if bearer(r) != "" {
			writeErrCode(w, http.StatusForbidden, "tokens_not_allowed", "API tokens can't be used here")
			return
		}
		h.ServeHTTP(w, r)
	})
}

// ---- OpenAPI -----------------------------------------------------------------

func (s *Server) handleOpenAPI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Access-Control-Allow-Origin", "*") // a description, no data: tools may load it
	writeJSON(w, 200, s.openAPI())
}

func (s *Server) openAPI() map[string]any {
	schemas := map[string]any{}
	gen := &schemaGen{defs: schemas}
	paths := map[string]map[string]any{}
	tags := map[string]bool{}
	for _, rt := range s.v1Routes() {
		op := map[string]any{"summary": rt.Summary, "tags": []string{rt.Tag},
			"operationId": strings.ToLower(rt.Method) + strings.NewReplacer("/", "_", "{", "", "}", "", ".", "_").Replace(rt.Path)}
		tags[rt.Tag] = true
		desc := rt.Description
		if rt.Method != "GET" {
			desc = strings.TrimSpace(desc + " Tokens need the write scope.")
		}
		if desc != "" {
			op["description"] = desc
		}
		var params []any
		for _, seg := range strings.Split(rt.Path, "/") {
			if strings.HasPrefix(seg, "{") {
				params = append(params, map[string]any{"name": strings.Trim(seg, "{}"), "in": "path", "required": true, "schema": map[string]any{"type": "integer"}})
			}
		}
		for _, q := range rt.Query {
			p := map[string]any{"name": q.Name, "in": "query", "schema": map[string]any{"type": q.Type}}
			if q.Description != "" {
				p["description"] = q.Description
			}
			params = append(params, p)
		}
		if rt.Paged {
			params = append(params,
				map[string]any{"name": "limit", "in": "query", "description": "entries per page (default 100, max. 1000)",
					"schema": map[string]any{"type": "integer", "minimum": 1, "maximum": pageMax, "default": pageDefault}},
				map[string]any{"name": "offset", "in": "query", "description": "entries to skip",
					"schema": map[string]any{"type": "integer", "minimum": 0, "default": 0}})
			var names []string
			fields := filterFields(rt)
			for n := range fields {
				names = append(names, n)
			}
			sort.Strings(names)
			for _, n := range names {
				ty := map[reflect.Kind]string{reflect.String: "string", reflect.Bool: "boolean"}[fields[n]]
				if ty == "" {
					ty = "integer"
				}
				params = append(params, map[string]any{"name": n, "in": "query",
					"description": "filter: only entries whose " + n + " equals this value (comma-separated: any of them; text ignores case)",
					"schema":      map[string]any{"type": "string"}, "x-field-type": ty})
			}
		}
		if rt.Method != "GET" {
			params = append(params, map[string]any{"name": "Idempotency-Key", "in": "header",
				"description": "Optional, up to 255 characters. A retry with the same key and the same request returns the stored response (header Idempotent-Replayed: true) instead of running again; the same key with a different request is refused (422). Keys are kept 24 hours.",
				"schema":      map[string]any{"type": "string", "maxLength": 255}})
		}
		if params != nil {
			op["parameters"] = params
		}
		switch {
		case rt.Body != nil:
			op["requestBody"] = map[string]any{"required": true, "content": map[string]any{
				"application/json": map[string]any{"schema": gen.schema(reflect.TypeOf(rt.Body))}}}
		case rt.Multipart != "":
			op["requestBody"] = map[string]any{"required": true, "description": rt.Multipart, "content": map[string]any{
				"multipart/form-data": map[string]any{"schema": map[string]any{"type": "object"}}}}
		}
		status := rt.Status
		if status == 0 {
			status = 200
		}
		ok := map[string]any{"description": "OK"}
		if rt.Paged {
			ok["headers"] = map[string]any{
				"X-Total-Count": map[string]any{"description": "entries matching the filters, across all pages", "schema": map[string]any{"type": "integer"}},
				"Link":          map[string]any{"description": `next and previous page: <…>; rel="next", <…>; rel="prev"`, "schema": map[string]any{"type": "string"}},
			}
		}
		switch {
		case rt.Produces != "":
			ok["content"] = map[string]any{rt.Produces: map[string]any{"schema": map[string]any{"type": "string", "format": "binary"}}}
		case rt.Resp != nil:
			ok["content"] = map[string]any{"application/json": map[string]any{"schema": gen.schema(reflect.TypeOf(rt.Resp))}}
		default:
			ok["content"] = map[string]any{"application/json": map[string]any{"schema": gen.schema(reflect.TypeOf(apiOK{}))}}
		}
		errResp := func(d string) any {
			return map[string]any{"description": d, "content": map[string]any{"application/json": map[string]any{"schema": gen.schema(reflect.TypeOf(apiErrorBody{}))}}}
		}
		responses := map[string]any{
			itoaInt(status): ok,
			"400":           errResp("Invalid request"),
			"401":           errResp("No valid token or session"),
			"403":           errResp("Not allowed (read-only token, not an administrator)"),
			"404":           errResp("Not found"),
			"429":           errResp("Rate limit reached – see Retry-After"),
		}
		if rt.Method != "GET" {
			responses["409"] = errResp("Conflict with the current state")
			responses["422"] = errResp("Idempotency-Key reused for a different request")
		}
		if rt.Multipart != "" {
			responses["413"] = errResp("Upload too large")
		}
		op["responses"] = responses
		p := "/api/v1" + rt.Path
		if paths[p] == nil {
			paths[p] = map[string]any{}
		}
		paths[p][strings.ToLower(rt.Method)] = op
	}
	var tagList []any
	var names []string
	for t := range tags {
		names = append(names, t)
	}
	sort.Strings(names)
	for _, t := range names {
		tagList = append(tagList, map[string]any{"name": t})
	}
	// the error codes are part of the contract
	if e, ok := schemas["Error"].(map[string]any); ok {
		var codes []string
		for c := range errorCodes {
			codes = append(codes, c)
		}
		sort.Strings(codes)
		var desc []string
		for _, c := range codes {
			desc = append(desc, "`"+c+"`: "+errorCodes[c])
		}
		e["properties"].(map[string]any)["code"] = map[string]any{"type": "string", "enum": codes,
			"description": "Stable, machine-readable. New codes may be added; treat unknown ones by their HTTP status.\n\n" + strings.Join(desc, "\n\n")}
	}
	// events: OpenAPI 3.1 webhooks
	hooks := map[string]any{}
	evSchema := gen.schema(reflect.TypeOf(webhookEvent{}))
	for _, t := range sortedEventTypes() {
		hooks[t] = map[string]any{"post": map[string]any{
			"summary":     t,
			"description": webhookEvents[t] + " " + webhookDataDocs[t],
			"parameters": []any{
				map[string]any{"name": "X-Typehaus-Event", "in": "header", "required": true, "schema": map[string]any{"type": "string", "const": t}},
				map[string]any{"name": "X-Typehaus-Delivery", "in": "header", "required": true, "description": "delivery id – the same on retries", "schema": map[string]any{"type": "string"}},
				map[string]any{"name": "X-Typehaus-Timestamp", "in": "header", "required": true, "description": "unix seconds", "schema": map[string]any{"type": "string"}},
				map[string]any{"name": "X-Typehaus-Signature", "in": "header", "required": true,
					"description": "v1=<hex HMAC-SHA256 of \"<timestamp>.<raw body>\" with the webhook's secret>. Reject deliveries with a wrong signature or a timestamp older than 5 minutes.",
					"schema":      map[string]any{"type": "string"}},
			},
			"requestBody": map[string]any{"required": true, "content": map[string]any{"application/json": map[string]any{"schema": evSchema}}},
			"responses":   map[string]any{"2XX": map[string]any{"description": "Delivered. Any other answer (or none within 10 s) is retried for about 22 hours."}},
		}}
	}
	serverURL := s.cfg.BaseURL
	if serverURL == "" {
		serverURL = "/" // relative to wherever the description was loaded from
	}
	return map[string]any{
		"openapi": "3.1.0",
		"info": map[string]any{
			"title":   "typehaus API",
			"version": "1",
			"description": "The stable interface of typehaus " + Version + ". Within v1, changes are compatible: new endpoints and fields may appear, " +
				"existing ones keep their meaning. Authenticate with an API token created by an administrator (web UI → API): " +
				"`Authorization: Bearer thk_…`. Read tokens may only GET. Times are Unix seconds.",
		},
		"servers":    []any{map[string]any{"url": serverURL}},
		"security":   []any{map[string]any{"token": []string{}}},
		"tags":       tagList,
		"paths":      paths,
		"webhooks":   hooks,
		"components": map[string]any{"schemas": schemas, "securitySchemes": map[string]any{"token": map[string]any{"type": "http", "scheme": "bearer"}}},
	}
}

func itoaInt(i int) string { b, _ := json.Marshal(i); return string(b) }

// schemaGen derives JSON Schemas from Go types (json tags; pointers are
// nullable; omitempty fields optional).
type schemaGen struct{ defs map[string]any }

var typeNames = map[string]string{
	"fontRow": "Font", "fontDetail": "FontDetail", "uploadResult": "UploadResult", "fontUpdate": "FontVersion",
	"collectionRow": "Collection", "collectionReq": "CollectionChange", "licenseRow": "License", "licenseReq": "LicenseChange",
	"licenseDetail": "LicenseDetail", "licensee": "Licensee", "licenseFamily": "LicenseFamily", "licenseHolder": "SeatHolder", "licenseDoc": "LicenseDocument",
	"usagePoint": "UsageDay", "fontRequest": "FontRequest", "userRow": "User", "userReq": "UserChange", "groupRow": "Group",
	"groupReq": "GroupChange", "deviceRow": "Device", "inventoryResult": "Inventory", "inventoryFinding": "InventoryFinding",
	"inventoryCopy": "InventoryCopy", "usageRow": "FamilyUsage", "auditEntry": "ActivityEntry", "apiErrorBody": "Error", "apiOK": "OK",
	"apiCreated": "Created", "apiFontIDs": "FontIDs", "apiAssignLicense": "AssignLicense", "apiUploadResponse": "UploadResponse",
	"apiDocsAdded": "DocumentsAdded", "apiDocReplaced": "DocumentReplaced", "apiDocUpdate": "DocumentChange", "apiRequestAnswer": "RequestAnswer",
	"webhookRow": "Webhook", "webhookReq": "WebhookChange", "webhookCreated": "WebhookSecret", "webhookDelivery": "WebhookDelivery",
	"webhookEventInfo": "WebhookEventType", "webhookEvent": "WebhookEvent",
	"catalogFamily": "CatalogFamily", "catalogImportReq": "CatalogImport", "catalogImportResult": "CatalogImportResult",
	"webkitRow": "WebKit", "webkitReq": "WebKitChange", "webkitCreated": "WebKitCreated", "webkitFont": "WebKitFont",
	"lookupResponse": "Lookup", "libraryMatch": "LibraryMatch", "identifyResponse": "Identification",
	"identifyServiceResult": "IdentificationResult", "identifyMatch": "IdentificationMatch",
}

func (g *schemaGen) schema(t reflect.Type) map[string]any {
	switch t.Kind() {
	case reflect.Pointer:
		return g.schema(t.Elem())
	case reflect.Slice:
		if t.Elem().Kind() == reflect.Uint8 {
			return map[string]any{"type": "string", "format": "byte"}
		}
		return map[string]any{"type": "array", "items": g.schema(t.Elem())}
	case reflect.Map:
		return map[string]any{"type": "object", "additionalProperties": g.schema(t.Elem())}
	case reflect.String:
		return map[string]any{"type": "string"}
	case reflect.Bool:
		return map[string]any{"type": "boolean"}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64, reflect.Uint, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return map[string]any{"type": "integer"}
	case reflect.Float32, reflect.Float64:
		return map[string]any{"type": "number"}
	case reflect.Interface:
		return map[string]any{}
	case reflect.Struct:
		name := typeNames[t.Name()]
		if name == "" {
			name = t.Name()
		}
		ref := map[string]any{"$ref": "#/components/schemas/" + name}
		if _, ok := g.defs[name]; ok {
			return ref
		}
		g.defs[name] = map[string]any{} // recursion guard
		props := map[string]any{}
		var required []string
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			tag := f.Tag.Get("json")
			if !f.IsExported() || tag == "-" {
				continue
			}
			parts := strings.Split(tag, ",")
			jn := parts[0]
			if jn == "" {
				jn = f.Name
			}
			sch := g.schema(f.Type)
			if f.Type.Kind() == reflect.Pointer {
				if ty, ok := sch["type"]; ok {
					sch = map[string]any{"type": []any{ty, "null"}}
					if it, ok := g.schema(f.Type.Elem())["items"]; ok {
						sch["items"] = it
					}
				} else {
					sch = map[string]any{"anyOf": []any{sch, map[string]any{"type": "null"}}}
				}
			}
			props[jn] = sch
			if !strings.Contains(tag, "omitempty") && f.Type.Kind() != reflect.Pointer {
				required = append(required, jn)
			}
		}
		def := map[string]any{"type": "object", "properties": props}
		if len(required) > 0 {
			def["required"] = required
		}
		g.defs[name] = def
		return ref
	}
	return map[string]any{}
}
