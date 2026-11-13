package server

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"sort"
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
	apiError struct {
		Error string `json:"error"`
	}
)

func (s *Server) v1Routes() []apiRoute {
	return []apiRoute{
		// fonts
		{Method: "GET", Path: "/fonts", H: s.handleListFonts, Tag: "Fonts", Summary: "List fonts",
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
		{Method: "GET", Path: "/collections", H: s.handleListCollections, Tag: "Collections", Summary: "List collections", Resp: []collectionRow{}},
		{Method: "POST", Path: "/collections", Admin: true, H: s.handleCreateCollection, Tag: "Collections", Summary: "Create a collection",
			Body: collectionReq{}, Resp: apiCreated{}, Status: 201},
		{Method: "PATCH", Path: "/collections/{id}", Admin: true, H: s.handleUpdateCollection, Tag: "Collections", Summary: "Change a collection",
			Description: "mode: available (people switch fonts on) or installed (installed automatically for everyone it applies to). Refused with 409 when an enforced license would run out of seats.",
			Body:        collectionReq{}},
		{Method: "DELETE", Path: "/collections/{id}", Admin: true, H: s.handleDeleteCollection, Tag: "Collections", Summary: "Delete a collection"},
		{Method: "POST", Path: "/collections/{id}/fonts", Admin: true, H: s.handleCollectionAddFonts, Tag: "Collections", Summary: "Add fonts to a collection", Body: apiFontIDs{}},
		{Method: "DELETE", Path: "/collections/{id}/fonts", Admin: true, H: s.handleCollectionRemoveFonts, Tag: "Collections", Summary: "Remove fonts from a collection", Body: apiFontIDs{}},

		// licenses
		{Method: "GET", Path: "/licenses", H: s.handleListLicenses, Tag: "Licenses", Summary: "List licenses", Resp: []licenseRow{}},
		{Method: "GET", Path: "/licenses/{id}", H: s.handleGetLicense, Tag: "Licenses", Summary: "Get a license",
			Description: "With its families and (admins and tokens) the people or computers holding a seat.", Resp: licenseDetail{}},
		{Method: "POST", Path: "/licenses", Admin: true, H: s.handleCreateLicense, Tag: "Licenses", Summary: "Create a license",
			Body: licenseReq{}, Resp: apiCreated{}, Status: 201},
		{Method: "PATCH", Path: "/licenses/{id}", Admin: true, H: s.handleUpdateLicense, Tag: "Licenses", Summary: "Change a license", Body: licenseReq{}},
		{Method: "DELETE", Path: "/licenses/{id}", Admin: true, H: s.handleDeleteLicense, Tag: "Licenses", Summary: "Delete a license",
			Description: "Its documents are deleted; its fonts stay in the library without a license."},
		{Method: "GET", Path: "/licenses/{id}/history", Admin: true, H: s.handleLicenseHistory, Tag: "Licenses", Summary: "Daily usage of a license",
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
		{Method: "GET", Path: "/requests", H: s.handleListRequests, Tag: "Font requests", Summary: "List font requests",
			Description: "Admins and tokens: everyone's requests.", Resp: []fontRequest{}},
		{Method: "PATCH", Path: "/requests/{id}", Admin: true, H: s.handleUpdateRequest, Tag: "Font requests", Summary: "Answer a font request", Body: apiRequestAnswer{}},

		// people
		{Method: "GET", Path: "/users", Admin: true, H: s.handleListUsers, Tag: "People", Summary: "List users", Resp: []userRow{}},
		{Method: "POST", Path: "/users", Admin: true, H: s.handleCreateUser, Tag: "People", Summary: "Create a user", Body: userReq{}, Resp: apiCreated{}, Status: 201},
		{Method: "PATCH", Path: "/users/{id}", Admin: true, H: s.handleUpdateUser, Tag: "People", Summary: "Change a user",
			Description: "disabled: true removes the fonts from the user's computers until enabled again.", Body: userReq{}},
		{Method: "DELETE", Path: "/users/{id}", Admin: true, H: s.handleDeleteUser, Tag: "People", Summary: "Delete a user"},
		{Method: "GET", Path: "/groups", Admin: true, H: s.handleListGroups, Tag: "People", Summary: "List groups", Resp: []groupRow{}},
		{Method: "POST", Path: "/groups", Admin: true, H: s.handleCreateGroup, Tag: "People", Summary: "Create a group", Body: groupReq{}, Resp: apiCreated{}, Status: 201},
		{Method: "PATCH", Path: "/groups/{id}", Admin: true, H: s.handleUpdateGroup, Tag: "People", Summary: "Change a group", Body: groupReq{}},
		{Method: "DELETE", Path: "/groups/{id}", Admin: true, H: s.handleDeleteGroup, Tag: "People", Summary: "Delete a group"},

		// devices
		{Method: "GET", Path: "/devices", H: s.handleListDevices, Tag: "Devices", Summary: "List computers",
			Query: []apiParam{{"all", "integer", "1: every computer (admins and tokens); otherwise only the signed-in user's"}}, Resp: []deviceRow{}},
		{Method: "DELETE", Path: "/devices/{id}", H: s.handleDeleteDevice, Tag: "Devices", Summary: "Disconnect a computer",
			Description: "The computer removes its typehaus fonts and forgets the connection."},

		// compliance
		{Method: "GET", Path: "/inventory", Admin: true, H: s.handleInventory, Tag: "Compliance", Summary: "Copies of library fonts found outside typehaus", Resp: inventoryResult{}},
		{Method: "GET", Path: "/inventory.csv", Admin: true, H: s.handleInventoryCSV, Tag: "Compliance", Summary: "Copies found outside typehaus (CSV)", Produces: "text/csv"},
		{Method: "GET", Path: "/reports/usage", Admin: true, H: s.handleUsageReport, Tag: "Compliance", Summary: "Usage by family", Resp: []usageRow{}},
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
				writeErr(w, http.StatusUnauthorized, err.Error())
				return
			}
			if r.Method != http.MethodGet && r.Method != http.MethodHead && u.Token.Scope != "write" {
				writeErr(w, http.StatusForbidden, "this token may only read")
				return
			}
			rt.H(w, r, u)
			return
		}
		u := s.sessionUser(r)
		if u == nil {
			writeErr(w, http.StatusUnauthorized, "not logged in – send an API token as Authorization: Bearer …")
			return
		}
		if rt.Admin && !u.IsAdmin {
			writeErr(w, http.StatusForbidden, "admin only")
			return
		}
		rt.H(w, r, u)
	})
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
}

func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if len(h) > 7 && strings.EqualFold(h[:7], "bearer ") {
		return strings.TrimSpace(h[7:])
	}
	return ""
}

var tokenUse sync.Map // token id -> last recorded use (unix)

// tokenUser resolves an API token to an administrator-level principal. It
// stops working when it expires, is revoked, or its creator is disabled or
// no longer an administrator.
func (s *Server) tokenUser(r *http.Request) (*authUser, error) {
	tok := bearer(r)
	if !strings.HasPrefix(tok, tokenPrefix) {
		return nil, errors.New("not a typehaus API token")
	}
	var t apiToken
	var uid int64
	var exp sql.NullInt64
	var admin, disabled bool
	err := s.db.QueryRow(`SELECT t.id, t.name, t.scope, t.expires_at, u.id, u.is_admin, u.disabled FROM api_tokens t
		JOIN users u ON u.id = t.created_by WHERE t.token_hash = ?`, hashToken(tok)).
		Scan(&t.ID, &t.Name, &t.Scope, &exp, &uid, &admin, &disabled)
	if err != nil {
		return nil, errors.New("unknown or revoked API token")
	}
	if exp.Valid && exp.Int64 < now() {
		return nil, errors.New("this API token has expired")
	}
	if !admin || disabled {
		return nil, errors.New("the administrator who created this token is no longer active")
	}
	n := now()
	if last, ok := tokenUse.Load(t.ID); !ok || n-last.(int64) >= 60 {
		tokenUse.Store(t.ID, n)
		s.db.Exec(`UPDATE api_tokens SET last_used_at = ?, last_ip = ? WHERE id = ?`, n, s.clientIP(r), t.ID)
	}
	return &authUser{ID: uid, Username: "token:" + t.Name, DisplayName: t.Name, IsAdmin: true, Token: &t}, nil
}

func (s *Server) handleListTokens(w http.ResponseWriter, r *http.Request, u *authUser) {
	rows, err := s.db.Query(`SELECT t.id, t.name, t.prefix, t.scope, COALESCE(NULLIF(u.display_name, ''), u.username),
		t.created_at, t.expires_at, t.last_used_at, t.last_ip FROM api_tokens t JOIN users u ON u.id = t.created_by ORDER BY t.created_at DESC`)
	if err != nil {
		s.internalErr(w, err)
		return
	}
	defer rows.Close()
	out := []apiToken{}
	for rows.Next() {
		var t apiToken
		var exp, used sql.NullInt64
		rows.Scan(&t.ID, &t.Name, &t.Prefix, &t.Scope, &t.CreatedBy, &t.CreatedAt, &exp, &used, &t.LastIP)
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

// noTokens keeps API tokens out of an endpoint (session only).
func (s *Server) noTokens(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if bearer(r) != "" {
			writeErr(w, http.StatusForbidden, "API tokens can't be used here")
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
		switch {
		case rt.Produces != "":
			ok["content"] = map[string]any{rt.Produces: map[string]any{"schema": map[string]any{"type": "string", "format": "binary"}}}
		case rt.Resp != nil:
			ok["content"] = map[string]any{"application/json": map[string]any{"schema": gen.schema(reflect.TypeOf(rt.Resp))}}
		default:
			ok["content"] = map[string]any{"application/json": map[string]any{"schema": gen.schema(reflect.TypeOf(apiOK{}))}}
		}
		errResp := func(d string) any {
			return map[string]any{"description": d, "content": map[string]any{"application/json": map[string]any{"schema": gen.schema(reflect.TypeOf(apiError{}))}}}
		}
		op["responses"] = map[string]any{
			itoaInt(status): ok,
			"400":           errResp("Invalid request"),
			"401":           errResp("No valid token or session"),
			"403":           errResp("Not allowed (read-only token, not an administrator)"),
			"404":           errResp("Not found"),
		}
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
	"licenseDetail": "LicenseDetail", "licenseFamily": "LicenseFamily", "licenseHolder": "SeatHolder", "licenseDoc": "LicenseDocument",
	"usagePoint": "UsageDay", "fontRequest": "FontRequest", "userRow": "User", "userReq": "UserChange", "groupRow": "Group",
	"groupReq": "GroupChange", "deviceRow": "Device", "inventoryResult": "Inventory", "inventoryFinding": "InventoryFinding",
	"inventoryCopy": "InventoryCopy", "usageRow": "FamilyUsage", "auditEntry": "ActivityEntry", "apiError": "Error", "apiOK": "OK",
	"apiCreated": "Created", "apiFontIDs": "FontIDs", "apiAssignLicense": "AssignLicense", "apiUploadResponse": "UploadResponse",
	"apiDocsAdded": "DocumentsAdded", "apiDocReplaced": "DocumentReplaced", "apiDocUpdate": "DocumentChange", "apiRequestAnswer": "RequestAnswer",
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
