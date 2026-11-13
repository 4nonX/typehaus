package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"testing"
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
		found = found || (e.Actor == "token:Provisioning" && e.Action == "collection.create")
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
			if m != "{id}" && m != "{doc}" {
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
