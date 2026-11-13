package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"
)

// apiCall sends a request with an API token (no session, no X-Typehaus).
func apiCall(t *testing.T, base, tok, method, path string, body any) (int, []byte, http.Header) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, base+path, rd)
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b, resp.Header
}

func newToken(t *testing.T, admin *client, name, scope string) string {
	t.Helper()
	var r map[string]any
	if st := admin.do("POST", "/api/tokens", map[string]any{"name": name, "scope": scope}, &r); st != 201 {
		t.Fatalf("create token: %d", st)
	}
	return r["token"].(string)
}

// Tokens: read vs write, only on /api/v1, can't manage tokens, revocation,
// creator losing admin rights, and no admin exceptions for downloads.
func TestAPITokens(t *testing.T) {
	_, ts, admin := newTestServer(t)
	uploadFiles(t, admin, map[string][]byte{"Poppins-Regular.ttf": fixture(t, "Poppins-Regular.ttf")})
	ro, rw := newToken(t, admin, "Reporting", "read"), newToken(t, admin, "Provisioning", "write")
	if !strings.HasPrefix(ro, "thk_") {
		t.Fatalf("token format: %q", ro)
	}

	if st, _, _ := apiCall(t, ts.URL, ro, "GET", "/api/v1/fonts", nil); st != 200 {
		t.Fatalf("read token GET: %d", st)
	}
	if st, _, _ := apiCall(t, ts.URL, ro, "POST", "/api/v1/collections", map[string]any{"name": "X"}); st != 403 {
		t.Fatalf("read token POST must be refused: %d", st)
	}
	st, b, _ := apiCall(t, ts.URL, rw, "POST", "/api/v1/collections", map[string]any{"name": "From the API"})
	if st != 201 {
		t.Fatalf("write token POST (no X-Typehaus needed): %d %s", st, b)
	}
	if st, _, _ := apiCall(t, ts.URL, "thk_wrong", "GET", "/api/v1/fonts", nil); st != 401 {
		t.Fatalf("bad token: %d", st)
	}
	if st, _, _ := apiCall(t, ts.URL, "", "GET", "/api/v1/fonts", nil); st != 401 {
		t.Fatalf("no auth: %d", st)
	}
	// only /api/v1: the web UI's endpoints and token management ignore tokens
	for _, p := range []string{"/api/fonts", "/api/me", "/api/tokens"} {
		if st, _, _ := apiCall(t, ts.URL, rw, "GET", p, nil); st != 401 && st != 403 {
			t.Fatalf("%s with a token: %d", p, st)
		}
	}
	if st, _, _ := apiCall(t, ts.URL, rw, "POST", "/api/tokens", map[string]any{"name": "x", "scope": "write"}); st != 403 && st != 401 {
		t.Fatalf("token creating tokens: %d", st)
	}
	// the audit log names the token
	var log []auditEntry
	admin.do("GET", "/api/audit", nil, &log)
	found := false
	for _, e := range log {
		if e.Actor == "token:Provisioning" && e.Action == "collection.create" {
			found = e.TokenID != nil && *e.TokenID > 0 && e.IP == "127.0.0.1"
		}
	}
	if !found {
		t.Fatalf("audit should name the token: %+v", log[:min(5, len(log))])
	}
	// the list shows usage, never the secret
	var toks []apiToken
	admin.do("GET", "/api/tokens", nil, &toks)
	if len(toks) != 2 || toks[0].LastUsed == nil || strings.Contains(fmt.Sprint(toks), rw) {
		t.Fatalf("token list: %+v", toks)
	}

	// downloads: a commercial license forbids them – no admin exception for tokens
	var fonts []fontRow
	admin.do("GET", "/api/fonts", nil, &fonts)
	var lic map[string]int64
	admin.do("POST", "/api/licenses", map[string]any{"name": "Commercial", "kind": "commercial"}, &lic)
	admin.do("PATCH", "/api/fonts", map[string]any{"font_ids": []int64{fonts[0].ID}, "license_id": lic["id"]}, nil)
	if st, _, _ := apiCall(t, ts.URL, ro, "GET", "/api/v1/fonts/download?ids="+itoa(fonts[0].ID), nil); st != 403 {
		t.Fatalf("token download of a no-download license: %d", st)
	}
	admin.do("PATCH", "/api/licenses/"+itoa(lic["id"]), map[string]any{"downloadable": true}, nil)
	if st, _, _ := apiCall(t, ts.URL, ro, "GET", "/api/v1/fonts/download?ids="+itoa(fonts[0].ID), nil); st != 200 {
		t.Fatalf("token download allowed by the license: %d", st)
	}

	// revoked
	admin.do("DELETE", "/api/tokens/"+itoa(toks[1].ID), nil, nil)
	revoked := ro
	if toks[1].Name == "Provisioning" {
		revoked = rw
	}
	if st, _, _ := apiCall(t, ts.URL, revoked, "GET", "/api/v1/fonts", nil); st != 401 {
		t.Fatalf("revoked token: %d", st)
	}
	// a token stops when its creator is no longer an administrator
	var me map[string]any
	admin.do("GET", "/api/me", nil, &me)
	var other map[string]int64
	admin.do("POST", "/api/users", map[string]any{"username": "boss", "password": "bosspassword1", "is_admin": true}, &other)
	boss := newClient(t, ts.URL)
	boss.do("POST", "/api/auth/login", map[string]string{"username": "boss", "password": "bosspassword1"}, nil)
	bt := newToken(t, boss, "Boss tool", "read")
	admin.do("PATCH", "/api/users/"+itoa(other["id"]), map[string]any{"is_admin": false}, nil)
	if st, _, _ := apiCall(t, ts.URL, bt, "GET", "/api/v1/fonts", nil); st != 401 {
		t.Fatalf("token of a former admin: %d", st)
	}
}

// The contract: every v1 route is in the OpenAPI description and every
// JSON response of the GET routes matches its schema.
func TestAPIv1Contract(t *testing.T) {
	_, ts, admin := newTestServer(t)
	uploadFiles(t, admin, map[string][]byte{"Poppins-Regular.ttf": fixture(t, "Poppins-Regular.ttf")})
	var fonts []fontRow
	admin.do("GET", "/api/fonts", nil, &fonts)
	var lic, col map[string]int64
	admin.do("POST", "/api/licenses", map[string]any{"name": "Brand", "kind": "commercial", "seats": 2}, &lic)
	admin.do("PATCH", "/api/fonts", map[string]any{"font_ids": []int64{fonts[0].ID}, "license_id": lic["id"]}, nil)
	uploadDoc(t, admin, lic["id"], "EULA.pdf", []byte("%PDF"), true)
	admin.do("POST", "/api/collections", map[string]any{"name": "All", "everyone": true}, &col)
	admin.do("POST", "/api/collections/"+itoa(col["id"])+"/fonts", map[string]any{"font_ids": []int64{fonts[0].ID}}, nil)
	admin.do("POST", "/api/groups", map[string]any{"name": "Design"}, nil)
	admin.do("POST", "/api/requests", map[string]any{"font": "Futura", "purpose": "Brochure"}, nil)
	admin.do("POST", "/api/activations", map[string]any{"font_ids": []int64{fonts[0].ID}}, nil)
	enrollDevice(t, ts, admin)
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer sink.Close()
	var hook webhookCreated
	admin.do("POST", "/api/webhooks", map[string]any{"name": "Sink", "url": sink.URL}, &hook)
	admin.do("POST", "/api/webhooks/"+itoa(hook.ID)+"/test", nil, nil)
	waitDelivered(t, admin, hook.ID)
	tok := newToken(t, admin, "Contract", "read")

	st, b, _ := apiCall(t, ts.URL, "", "GET", "/api/v1/openapi.json", nil)
	if st != 200 {
		t.Fatalf("openapi.json: %d", st)
	}
	var spec map[string]any
	if err := json.Unmarshal(b, &spec); err != nil {
		t.Fatal(err)
	}
	paths := spec["paths"].(map[string]any)
	schemas := spec["components"].(map[string]any)["schemas"].(map[string]any)

	srv := &Server{}
	routes := srv.v1Routes()
	n := 0
	for _, p := range paths {
		n += len(p.(map[string]any))
	}
	if n != len(routes) {
		t.Fatalf("spec has %d operations, server %d routes", n, len(routes))
	}
	ids := map[string]string{"{id}": itoa(fonts[0].ID)}
	for _, rt := range routes {
		op, ok := paths["/api/v1"+rt.Path].(map[string]any)[strings.ToLower(rt.Method)].(map[string]any)
		if !ok {
			t.Fatalf("%s %s not documented", rt.Method, rt.Path)
		}
		if rt.Method != "GET" || rt.Produces != "" || rt.Resp == nil {
			continue
		}
		path := rt.Path
		switch {
		case strings.HasPrefix(path, "/licenses/{id}"):
			path = strings.Replace(path, "{id}", itoa(lic["id"]), 1)
		case strings.HasPrefix(path, "/webhooks/{id}"):
			path = strings.Replace(path, "{id}", itoa(hook.ID), 1)
		default:
			path = strings.Replace(path, "{id}", ids["{id}"], 1)
		}
		q := ""
		if rt.Path == "/devices" {
			q = "?all=1"
		}
		st, body, _ := apiCall(t, ts.URL, tok, "GET", "/api/v1"+path+q, nil)
		if st != 200 {
			t.Fatalf("GET %s: %d %s", path, st, body)
		}
		var v any
		json.Unmarshal(body, &v)
		resp := op["responses"].(map[string]any)["200"].(map[string]any)
		sch := resp["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)
		if errs := validate(v, sch, schemas, rt.Path); len(errs) > 0 {
			sort.Strings(errs)
			t.Errorf("GET %s does not match its schema:\n  %s", rt.Path, strings.Join(errs, "\n  "))
		}
		if a, ok := v.([]any); ok && len(a) == 0 && rt.Path != "/inventory" && rt.Path != "/licenses/{id}/history" {
			t.Errorf("GET %s returned nothing – the check above proved little", rt.Path)
		}
	}
	// the description's examples: every path parameter is an integer
	re := regexp.MustCompile(`\{[a-z]+\}`)
	for p := range paths {
		for _, m := range re.FindAllString(p, -1) {
			if m != "{id}" && m != "{doc}" && m != "{delivery}" {
				t.Errorf("unexpected path parameter %s in %s", m, p)
			}
		}
	}
}

// validate checks a decoded JSON value against a (generated) JSON Schema.
func validate(v any, sch map[string]any, defs map[string]any, at string) []string {
	if ref, ok := sch["$ref"].(string); ok {
		return validate(v, defs[strings.TrimPrefix(ref, "#/components/schemas/")].(map[string]any), defs, at)
	}
	if alts, ok := sch["anyOf"].([]any); ok {
		for _, a := range alts {
			if len(validate(v, a.(map[string]any), defs, at)) == 0 {
				return nil
			}
		}
		return []string{at + ": matches none of anyOf"}
	}
	var types []string
	switch t := sch["type"].(type) {
	case string:
		types = []string{t}
	case []any:
		for _, x := range t {
			types = append(types, x.(string))
		}
	default:
		return nil
	}
	kind := "null"
	switch x := v.(type) {
	case bool:
		kind = "boolean"
	case float64:
		kind = "number"
		if x == float64(int64(x)) {
			kind = "integer"
		}
	case string:
		kind = "string"
	case []any:
		kind = "array"
	case map[string]any:
		kind = "object"
	}
	okType := false
	for _, t := range types {
		okType = okType || t == kind || (t == "number" && kind == "integer")
	}
	if !okType {
		return []string{fmt.Sprintf("%s: %s, want %v", at, kind, types)}
	}
	var errs []string
	switch kind {
	case "array":
		if it, ok := sch["items"].(map[string]any); ok {
			for i, e := range v.([]any) {
				errs = append(errs, validate(e, it, defs, fmt.Sprintf("%s[%d]", at, i))...)
				if i > 20 {
					break
				}
			}
		}
	case "object":
		obj := v.(map[string]any)
		props, _ := sch["properties"].(map[string]any)
		req, _ := sch["required"].([]any)
		for _, r := range req {
			if _, ok := obj[r.(string)]; !ok {
				errs = append(errs, at+"."+r.(string)+": missing")
			}
		}
		for k, pv := range obj {
			ps, ok := props[k].(map[string]any)
			if !ok {
				if props != nil {
					errs = append(errs, at+"."+k+": not in the schema")
				}
				continue
			}
			errs = append(errs, validate(pv, ps, defs, at+"."+k)...)
		}
		if ap, ok := sch["additionalProperties"].(map[string]any); ok {
			for k, pv := range obj {
				errs = append(errs, validate(pv, ap, defs, at+"."+k)...)
			}
		}
	}
	return errs
}

var _ = multipart.NewWriter

// Error contract: every v1 route with an id answers an unknown id with 404
// and code not_found, and every error body carries a known code.
func TestAPIv1NotFound(t *testing.T) {
	_, ts, admin := newTestServer(t)
	var lic map[string]int64
	admin.do("POST", "/api/licenses", map[string]any{"name": "L"}, &lic)
	tok := newToken(t, admin, "NF", "write")
	srv := &Server{}
	for _, rt := range srv.v1Routes() {
		if !strings.Contains(rt.Path, "{") {
			continue
		}
		path := strings.ReplaceAll(strings.ReplaceAll(rt.Path, "{id}", "999999"), "{delivery}", "999999")
		if strings.Contains(rt.Path, "{doc}") { // unknown document of an existing license
			path = strings.Replace(strings.ReplaceAll(rt.Path, "{doc}", "999999"), "{id}", itoa(lic["id"]), 1)
		}
		var body any
		if rt.Body != nil {
			sb := sampleBody(rt.Body)
			if _, isLic := rt.Body.(licenseReq); isLic {
				delete(sb, "kind") // licenses are commercial or open
			}
			body = sb
		}
		var st int
		var b []byte
		if rt.Multipart != "" {
			st, b = multipartCall(t, ts.URL, tok, rt.Method, "/api/v1"+path)
		} else {
			st, b, _ = apiCall(t, ts.URL, tok, rt.Method, "/api/v1"+path, body)
		}
		var e apiErrorBody
		json.Unmarshal(b, &e)
		if st != 404 || e.Code != "not_found" {
			t.Errorf("%s %s with an unknown id: %d %s", rt.Method, rt.Path, st, b)
		}
	}
}

// sampleBody fills a request type with plausible values.
func sampleBody(v any) map[string]any {
	out := map[string]any{}
	b, _ := json.Marshal(v)
	json.Unmarshal(b, &out)
	for k := range out {
		switch k {
		case "font_ids":
			out[k] = []int64{1}
		case "name", "filename", "answer", "username":
			out[k] = "x-" + k
		case "status":
			out[k] = "done"
		case "kind":
			out[k] = "other"
		default:
			delete(out, k)
		}
	}
	if len(out) == 0 {
		out["name"] = "x"
	}
	return out
}

func multipartCall(t *testing.T, base, tok, method, path string) (int, []byte) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("files", "a.pdf")
	fw.Write([]byte("%PDF"))
	mw.Close()
	req, _ := http.NewRequest(method, base+path, &buf)
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}

// Token lifecycle: expiry header, rotation with and without grace period,
// expired tokens, and the per-token rate limit.
func TestAPITokenLifecycle(t *testing.T) {
	s, ts, admin := newTestServer(t)
	tok := newToken(t, admin, "Rotating", "read")
	var toks []apiToken
	admin.do("GET", "/api/tokens", nil, &toks)
	id := toks[0].ID

	// expiry: set, visible in every response
	admin.do("PATCH", "/api/tokens/"+itoa(id), map[string]any{"expires_days": 30}, nil)
	_, _, h := apiCall(t, ts.URL, tok, "GET", "/api/v1/licenses", nil)
	if h.Get("X-Typehaus-Token-Expires") == "" {
		t.Fatal("expiry header missing")
	}

	// rotate with a grace period: old and new secret work, the old one says so
	var rot map[string]any
	if st := admin.do("POST", "/api/tokens/"+itoa(id)+"/rotate", map[string]any{"grace_hours": 24}, &rot); st != 200 {
		t.Fatalf("rotate: %d", st)
	}
	newTok := rot["token"].(string)
	st, _, h := apiCall(t, ts.URL, tok, "GET", "/api/v1/licenses", nil)
	if st != 200 || h.Get("X-Typehaus-Token-Superseded") == "" {
		t.Fatalf("old secret during grace: %d %q", st, h.Get("X-Typehaus-Token-Superseded"))
	}
	if st, _, h := apiCall(t, ts.URL, newTok, "GET", "/api/v1/licenses", nil); st != 200 || h.Get("X-Typehaus-Token-Superseded") != "" {
		t.Fatalf("new secret: %d", st)
	}
	// rotate again without grace: only the newest works
	admin.do("POST", "/api/tokens/"+itoa(id)+"/rotate", map[string]any{"grace_hours": 0}, &rot)
	newest := rot["token"].(string)
	for _, old := range []string{tok, newTok} {
		st, b, _ := apiCall(t, ts.URL, old, "GET", "/api/v1/licenses", nil)
		var e apiErrorBody
		json.Unmarshal(b, &e)
		if st != 401 || e.Code != "token_invalid" {
			t.Fatalf("replaced secret: %d %s", st, b)
		}
	}
	// expired
	s.db.Exec(`UPDATE api_tokens SET expires_at = ? WHERE id = ?`, now()-10, id)
	st, b, _ := apiCall(t, ts.URL, newest, "GET", "/api/v1/licenses", nil)
	var e apiErrorBody
	json.Unmarshal(b, &e)
	if st != 401 || e.Code != "token_expired" {
		t.Fatalf("expired: %d %s", st, b)
	}

	// rate limit: 12/minute → burst of 10, then 429 with Retry-After
	s.cfg.APIRate = 12
	rl := newToken(t, admin, "Busy", "read")
	got429 := 0
	var last http.Header
	for i := 0; i < 12; i++ {
		st, b, h := apiCall(t, ts.URL, rl, "GET", "/api/v1/collections", nil)
		if st == 429 {
			got429++
			json.Unmarshal(b, &e)
			last = h
		}
	}
	if got429 < 1 || e.Code != "rate_limited" || last.Get("Retry-After") == "" || last.Get("RateLimit-Limit") != "12" {
		t.Fatalf("rate limit: %d × 429, code %q, headers %v", got429, e.Code, last)
	}
	// other tokens have their own budget
	if st, _, _ := apiCall(t, ts.URL, newToken(t, admin, "Calm", "read"), "GET", "/api/v1/collections", nil); st != 200 {
		t.Fatalf("separate budget: %d", st)
	}
}

// Pagination and filters on v1 lists.
func TestAPIv1Pagination(t *testing.T) {
	_, ts, admin := newTestServer(t)
	for i := 0; i < 7; i++ {
		admin.do("POST", "/api/groups", map[string]any{"name": fmt.Sprintf("G%02d", i), "description": map[bool]string{true: "even", false: "odd"}[i%2 == 0]}, nil)
	}
	admin.do("POST", "/api/licenses", map[string]any{"name": "A", "kind": "commercial"}, nil)
	admin.do("POST", "/api/licenses", map[string]any{"name": "B", "kind": "open"}, nil)
	admin.do("POST", "/api/licenses", map[string]any{"name": "C", "kind": "commercial"}, nil)
	tok := newToken(t, admin, "Pager", "read")
	names := func(b []byte) []string {
		var v []map[string]any
		json.Unmarshal(b, &v)
		var out []string
		for _, x := range v {
			out = append(out, x["name"].(string))
		}
		return out
	}
	st, b, h := apiCall(t, ts.URL, tok, "GET", "/api/v1/groups?limit=3", nil)
	if st != 200 || h.Get("X-Total-Count") != "7" || fmt.Sprint(names(b)) != "[G00 G01 G02]" || !strings.Contains(h.Get("Link"), `offset=3>; rel="next"`) {
		t.Fatalf("page 1: %d %v %q %q", st, names(b), h.Get("X-Total-Count"), h.Get("Link"))
	}
	_, b, h = apiCall(t, ts.URL, tok, "GET", "/api/v1/groups?limit=3&offset=6", nil)
	if fmt.Sprint(names(b)) != "[G06]" || strings.Contains(h.Get("Link"), "next") || !strings.Contains(h.Get("Link"), "prev") {
		t.Fatalf("last page: %v %q", names(b), h.Get("Link"))
	}
	_, b, h = apiCall(t, ts.URL, tok, "GET", "/api/v1/groups?description=EVEN", nil)
	if h.Get("X-Total-Count") != "4" || len(names(b)) != 4 {
		t.Fatalf("filter (case-insensitive): %v", names(b))
	}
	_, b, _ = apiCall(t, ts.URL, tok, "GET", "/api/v1/licenses?kind=commercial", nil)
	if fmt.Sprint(names(b)) != "[A C]" {
		t.Fatalf("kind filter: %v", names(b))
	}
	_, b, _ = apiCall(t, ts.URL, tok, "GET", "/api/v1/licenses?kind=commercial,open&enforce=false", nil)
	if len(names(b)) != 3 {
		t.Fatalf("any-of + bool filter: %v", names(b))
	}
	for _, bad := range []string{"limit=0", "limit=5000", "offset=-1", "enforce=maybe", "seats=many"} {
		st, b, _ := apiCall(t, ts.URL, tok, "GET", "/api/v1/licenses?"+bad, nil)
		var e apiErrorBody
		json.Unmarshal(b, &e)
		if st != 400 || e.Code != "invalid_request" {
			t.Fatalf("%s: %d %s", bad, st, b)
		}
	}
	// default page size
	for i := 0; i < 105; i++ {
		admin.do("POST", "/api/groups", map[string]any{"name": fmt.Sprintf("Bulk%03d", i)}, nil)
	}
	_, b, h = apiCall(t, ts.URL, tok, "GET", "/api/v1/groups", nil)
	if len(names(b)) != 100 || h.Get("X-Total-Count") != "112" {
		t.Fatalf("default page: %d of %s", len(names(b)), h.Get("X-Total-Count"))
	}
	// activity log: cursor paging
	_, _, h = apiCall(t, ts.URL, tok, "GET", "/api/v1/audit?limit=5", nil)
	if !strings.Contains(h.Get("Link"), "before=") {
		t.Fatalf("audit cursor: %q", h.Get("Link"))
	}
}

// Idempotency-Key: a retried write runs once.
func TestAPIv1Idempotency(t *testing.T) {
	s, ts, admin := newTestServer(t)
	tok := newToken(t, admin, "Retry", "write")
	call := func(key string, body any) (int, []byte, http.Header) {
		b, _ := json.Marshal(body)
		req, _ := http.NewRequest("POST", ts.URL+"/api/v1/collections", bytes.NewReader(b))
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Content-Type", "application/json")
		if key != "" {
			req.Header.Set("Idempotency-Key", key)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		rb, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, rb, resp.Header
	}
	st1, b1, _ := call("order-7", map[string]any{"name": "Brand"})
	st2, b2, h2 := call("order-7", map[string]any{"name": "Brand"})
	if st1 != 201 || st2 != 201 || string(b1) != string(b2) || h2.Get("Idempotent-Replayed") != "true" {
		t.Fatalf("retry: %d %s / %d %s %q", st1, b1, st2, b2, h2.Get("Idempotent-Replayed"))
	}
	var n int
	s.db.QueryRow(`SELECT COUNT(*) FROM collections WHERE name = 'Brand'`).Scan(&n)
	if n != 1 {
		t.Fatalf("collections created: %d", n)
	}
	st, b, _ := call("order-7", map[string]any{"name": "Other"})
	var e apiErrorBody
	json.Unmarshal(b, &e)
	if st != 422 || e.Code != "idempotency_key_reused" {
		t.Fatalf("reused key: %d %s", st, b)
	}
	// an error result is replayed too (it was an answer, not a failure)
	st1, _, _ = call("dup-1", map[string]any{"name": "Brand"})
	st2, _, h2 = call("dup-1", map[string]any{"name": "Brand"})
	if st1 != 409 || st2 != 409 || h2.Get("Idempotent-Replayed") != "true" {
		t.Fatalf("replayed error: %d %d", st1, st2)
	}
	// still running
	s.db.Exec(`INSERT INTO api_idempotency (principal, key, method, path, body_hash, created_at) VALUES ('t1', 'busy', 'POST', '/api/v1/collections', 'x', 0)`)
	var toks []apiToken
	admin.do("GET", "/api/tokens", nil, &toks)
	s.db.Exec(`UPDATE api_idempotency SET principal = ? WHERE key = 'busy'`, "t"+itoa(toks[0].ID))
	st, b, _ = call("busy", map[string]any{"name": "X"})
	json.Unmarshal(b, &e)
	if st != 422 && (st != 409 || e.Code != "idempotency_in_progress") {
		t.Fatalf("in progress: %d %s", st, b)
	}
	// without a key nothing is remembered
	call("", map[string]any{"name": "NoKey"})
	if st, _, _ := call("", map[string]any{"name": "NoKey"}); st != 409 {
		t.Fatalf("no key: second create should conflict: %d", st)
	}
	// uploads: the same file twice with one key → one upload
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, _ := mw.CreateFormFile("files", "Poppins-Regular.ttf")
	fw.Write(fixture(t, "Poppins-Regular.ttf"))
	mw.Close()
	body := buf.Bytes()
	up := func() (int, string) {
		req, _ := http.NewRequest("POST", ts.URL+"/api/v1/fonts", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Content-Type", mw.FormDataContentType())
		req.Header.Set("Idempotency-Key", "upload-1")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		return resp.StatusCode, resp.Header.Get("Idempotent-Replayed")
	}
	if st, _ := up(); st != 200 {
		t.Fatalf("upload: %d", st)
	}
	if st, rep := up(); st != 200 || rep != "true" {
		t.Fatalf("upload retry: %d %q", st, rep)
	}
}

// waitDelivered waits until a webhook has no pending deliveries.
func waitDelivered(t *testing.T, admin *client, id int64) []webhookDelivery {
	t.Helper()
	for i := 0; i < 100; i++ {
		var ds []webhookDelivery
		admin.do("GET", "/api/webhooks/"+itoa(id)+"/deliveries", nil, &ds)
		pending := false
		for _, d := range ds {
			pending = pending || d.Status == "pending"
		}
		if len(ds) > 0 && !pending {
			return ds
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("webhook deliveries still pending")
	return nil
}
