package server

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
)

// The v1 API promises compatible changes only (see the description in
// openapi.json). testdata/openapi-v1.json is the published contract, as
// of 1.0.0; this test fails when the current spec breaks it:
//
//   - an operation, its success status, a parameter or a webhook event is gone
//   - a parameter or request field is newly required
//   - a request field is no longer accepted, or changes type
//   - a response field is gone, changes type, becomes nullable or optional
//   - an error code is gone
//
// Additions are fine. After a compatible change, refresh the snapshot with
//
//	go test ./internal/server -run TestAPICompat -update-api
//
// and commit it with the change.

var updateAPI = flag.Bool("update-api", false, "rewrite testdata/openapi-v1.json from the current spec")

const apiSnapshot = "testdata/openapi-v1.json"

func currentSpec(t *testing.T) map[string]any {
	t.Helper()
	b, err := json.Marshal((&Server{}).openAPI())
	if err != nil {
		t.Fatal(err)
	}
	var spec map[string]any
	if err := json.Unmarshal(b, &spec); err != nil {
		t.Fatal(err)
	}
	// the version string and descriptions are not part of the contract
	spec["info"].(map[string]any)["description"] = ""
	return spec
}

func TestAPICompat(t *testing.T) {
	cur := currentSpec(t)
	if *updateAPI {
		b, _ := json.MarshalIndent(cur, "", "  ")
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(apiSnapshot, append(b, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s", apiSnapshot)
		return
	}
	b, err := os.ReadFile(apiSnapshot)
	if err != nil {
		t.Fatalf("%v (create it with -update-api)", err)
	}
	var base map[string]any
	if err := json.Unmarshal(b, &base); err != nil {
		t.Fatal(err)
	}
	problems := apiBreaks(base, cur)
	for _, p := range problems {
		t.Error(p)
	}
	if len(problems) > 0 {
		t.Log("If a break is intended, it belongs in API v2, not v1.")
	}
}

// TestAPICompatDetects makes sure the checker catches the breaks it promises.
func TestAPICompatDetects(t *testing.T) {
	base := currentSpec(t)
	cases := []struct {
		name string
		edit func(spec map[string]any)
		want string
	}{
		{"operation removed", func(s map[string]any) {
			delete(s["paths"].(map[string]any)["/api/v1/fonts"].(map[string]any), "get")
		}, "GET /api/v1/fonts: removed"},
		{"response field removed", func(s map[string]any) {
			delete(schemaDef(s, "Font")["properties"].(map[string]any), "family")
		}, "family: removed"},
		{"response field type changed", func(s map[string]any) {
			schemaDef(s, "Font")["properties"].(map[string]any)["family"] = map[string]any{"type": "integer"}
		}, "family: type string → integer"},
		{"request field newly required", func(s map[string]any) {
			d := schemaDef(s, "CollectionChange")
			d["required"] = append(toStrings(d["required"]), "x_new")
			d["properties"].(map[string]any)["x_new"] = map[string]any{"type": "string"}
		}, "x_new: newly required"},
		{"query parameter removed", func(s map[string]any) {
			op := s["paths"].(map[string]any)["/api/v1/fonts"].(map[string]any)["get"].(map[string]any)
			op["parameters"] = op["parameters"].([]any)[1:]
		}, "parameter"},
		{"error code removed", func(s map[string]any) {
			e := schemaDef(s, "Error")["properties"].(map[string]any)["code"].(map[string]any)
			e["enum"] = toAny(toStrings(e["enum"])[1:])
		}, "error code"},
		{"webhook event removed", func(s map[string]any) {
			delete(s["webhooks"].(map[string]any), "font.added")
		}, "webhook font.added: removed"},
	}
	for _, c := range cases {
		cur := currentSpec(t)
		c.edit(cur)
		got := strings.Join(apiBreaks(base, cur), "\n")
		if !strings.Contains(got, c.want) {
			t.Errorf("%s: want a problem containing %q, got:\n%s", c.name, c.want, got)
		}
	}
	// additions are compatible
	cur := currentSpec(t)
	schemaDef(cur, "Font")["properties"].(map[string]any)["x_extra"] = map[string]any{"type": "string"}
	cur["paths"].(map[string]any)["/api/v1/x-new"] = map[string]any{"get": map[string]any{"responses": map[string]any{}}}
	if p := apiBreaks(base, cur); len(p) > 0 {
		t.Errorf("additions reported as breaks: %v", p)
	}
}

func schemaDef(spec map[string]any, name string) map[string]any {
	return spec["components"].(map[string]any)["schemas"].(map[string]any)[name].(map[string]any)
}

func toStrings(v any) []string {
	var out []string
	if a, ok := v.([]any); ok {
		for _, x := range a {
			if s, ok := x.(string); ok {
				out = append(out, s)
			}
		}
	}
	if a, ok := v.([]string); ok {
		out = append(out, a...)
	}
	return out
}

func toAny(s []string) []any {
	out := make([]any, len(s))
	for i, x := range s {
		out[i] = x
	}
	return out
}

func mapKeys(m map[string]any) []string {
	var k []string
	for x := range m {
		k = append(k, x)
	}
	sort.Strings(k)
	return k
}

type apiChecker struct {
	base, cur map[string]any // components.schemas
	problems  []string
	seen      map[string]bool
}

func (c *apiChecker) add(format string, a ...any) { c.problems = append(c.problems, fmt.Sprintf(format, a...)) }

func apiBreaks(base, cur map[string]any) []string {
	comp := func(s map[string]any) map[string]any {
		if c, ok := s["components"].(map[string]any); ok {
			if sc, ok := c["schemas"].(map[string]any); ok {
				return sc
			}
		}
		return map[string]any{}
	}
	c := &apiChecker{base: comp(base), cur: comp(cur), seen: map[string]bool{}}
	bp, _ := base["paths"].(map[string]any)
	cp, _ := cur["paths"].(map[string]any)
	for _, path := range mapKeys(bp) {
		bm := bp[path].(map[string]any)
		cm, _ := cp[path].(map[string]any)
		for _, method := range mapKeys(bm) {
			where := strings.ToUpper(method) + " " + path
			bo := bm[method].(map[string]any)
			co, ok := cm[method].(map[string]any)
			if !ok {
				c.add("%s: removed", where)
				continue
			}
			c.operation(where, bo, co)
		}
	}
	// error codes
	codes := func(sc map[string]any) []string {
		e, _ := sc["Error"].(map[string]any)
		p, _ := e["properties"].(map[string]any)
		code, _ := p["code"].(map[string]any)
		return toStrings(code["enum"])
	}
	have := map[string]bool{}
	for _, x := range codes(c.cur) {
		have[x] = true
	}
	for _, x := range codes(c.base) {
		if !have[x] {
			c.add("error code %q: removed", x)
		}
	}
	// webhooks
	bw, _ := base["webhooks"].(map[string]any)
	cw, _ := cur["webhooks"].(map[string]any)
	for _, ev := range mapKeys(bw) {
		if _, ok := cw[ev]; !ok {
			c.add("webhook %s: removed", ev)
			continue
		}
		c.seen = map[string]bool{}
		c.compare("webhook "+ev, bodySchema(bw[ev].(map[string]any)["post"].(map[string]any)), bodySchema(cw[ev].(map[string]any)["post"].(map[string]any)), false)
	}
	return c.problems
}

func bodySchema(op map[string]any) map[string]any {
	rb, _ := op["requestBody"].(map[string]any)
	content, _ := rb["content"].(map[string]any)
	j, _ := content["application/json"].(map[string]any)
	s, _ := j["schema"].(map[string]any)
	return s
}

func paramKey(p map[string]any) string { return fmt.Sprint(p["in"], ":", p["name"]) }

func (c *apiChecker) operation(where string, bo, co map[string]any) {
	// parameters
	cparams := map[string]map[string]any{}
	for _, p := range asSlice(co["parameters"]) {
		pm := p.(map[string]any)
		cparams[paramKey(pm)] = pm
	}
	bparams := map[string]bool{}
	for _, p := range asSlice(bo["parameters"]) {
		pm := p.(map[string]any)
		k := paramKey(pm)
		bparams[k] = true
		cpm, ok := cparams[k]
		if !ok {
			c.add("%s: parameter %s removed", where, k)
			continue
		}
		bt, ct := schemaType(pm["schema"]), schemaType(cpm["schema"])
		if bt != ct {
			c.add("%s: parameter %s type %s → %s", where, k, bt, ct)
		}
		if cpm["required"] == true && pm["required"] != true {
			c.add("%s: parameter %s newly required", where, k)
		}
	}
	for k, p := range cparams {
		if !bparams[k] && p["required"] == true && p["in"] != "path" {
			c.add("%s: new required parameter %s", where, k)
		}
	}
	// request body
	if bs := bodySchema(bo); bs != nil {
		cs := bodySchema(co)
		if cs == nil {
			c.add("%s: JSON request body no longer accepted", where)
		} else {
			c.seen = map[string]bool{}
			c.compare(where+" request", bs, cs, true)
		}
	} else if bodySchema(co) != nil {
		if rb, _ := co["requestBody"].(map[string]any); rb["required"] == true && bo["requestBody"] == nil {
			c.add("%s: request body newly required", where)
		}
	}
	// success response
	bres, _ := bo["responses"].(map[string]any)
	cres, _ := co["responses"].(map[string]any)
	for code, r := range bres {
		if !strings.HasPrefix(code, "2") {
			continue
		}
		cr, ok := cres[code].(map[string]any)
		if !ok {
			c.add("%s: success status %s changed", where, code)
			continue
		}
		bc, _ := r.(map[string]any)["content"].(map[string]any)
		cc, _ := cr["content"].(map[string]any)
		for mt, m := range bc {
			cm, ok := cc[mt].(map[string]any)
			if !ok {
				c.add("%s: response type %s gone", where, mt)
				continue
			}
			c.seen = map[string]bool{}
			bs, _ := m.(map[string]any)["schema"].(map[string]any)
			cs, _ := cm["schema"].(map[string]any)
			c.compare(where+" response", bs, cs, false)
		}
		bh, _ := r.(map[string]any)["headers"].(map[string]any)
		ch, _ := cr["headers"].(map[string]any)
		for h := range bh {
			if _, ok := ch[h]; !ok {
				c.add("%s: response header %s gone", where, h)
			}
		}
	}
}

func asSlice(v any) []any { s, _ := v.([]any); return s }

// resolve follows a $ref; it returns the schema and the ref name ("" inline).
func resolve(sc map[string]any, s map[string]any) (map[string]any, string) {
	if r, ok := s["$ref"].(string); ok {
		name := strings.TrimPrefix(r, "#/components/schemas/")
		d, _ := sc[name].(map[string]any)
		return d, name
	}
	return s, ""
}

// schemaType gives a comparable type: "string", "integer|null", "array", …
func schemaType(v any) string {
	s, _ := v.(map[string]any)
	if s == nil {
		return ""
	}
	if _, ok := s["$ref"]; ok {
		return "object"
	}
	switch t := s["type"].(type) {
	case string:
		return t
	case []any:
		var parts []string
		for _, x := range t {
			parts = append(parts, fmt.Sprint(x))
		}
		sort.Strings(parts)
		return strings.Join(parts, "|")
	}
	if alts, ok := s["anyOf"].([]any); ok {
		var parts []string
		for _, x := range alts {
			parts = append(parts, schemaType(x))
		}
		sort.Strings(parts)
		return strings.Join(parts, "|")
	}
	return ""
}

func nullable(t string) bool { return strings.Contains(t, "null") }
func stripNull(t string) string {
	var keep []string
	for _, p := range strings.Split(t, "|") {
		if p != "null" {
			keep = append(keep, p)
		}
	}
	return strings.Join(keep, "|")
}

// inner unwraps a nullable anyOf to the non-null schema.
func inner(s map[string]any) map[string]any {
	if alts, ok := s["anyOf"].([]any); ok {
		for _, x := range alts {
			m, _ := x.(map[string]any)
			if m["type"] != "null" {
				return m
			}
		}
	}
	return s
}

// compare checks schema cur against the contract base. In a request,
// clients send base-shaped data: every base field must still be accepted
// with the same type, and nothing new may be required. In a response,
// clients read base-shaped data: every base field must still be sent,
// with the same type, no newly null and no newly optional.
func (c *apiChecker) compare(where string, base, cur map[string]any, request bool) {
	if base == nil {
		return
	}
	if cur == nil {
		c.add("%s: schema gone", where)
		return
	}
	bt, ct := schemaType(base), schemaType(cur)
	if stripNull(bt) != stripNull(ct) && bt != "" && ct != "" {
		c.add("%s: type %s → %s", where, bt, ct)
		return
	}
	if request && nullable(bt) && !nullable(ct) {
		c.add("%s: no longer accepts null", where)
	}
	if !request && !nullable(bt) && nullable(ct) {
		c.add("%s: may now be null", where)
	}
	base, cur = inner(base), inner(cur)
	b, bname := resolve(c.base, base)
	cu, _ := resolve(c.cur, cur)
	if bname != "" {
		key := fmt.Sprint(bname, request)
		if c.seen[key] {
			return
		}
		c.seen[key] = true
	}
	if b == nil || cu == nil {
		return
	}
	if items, ok := b["items"].(map[string]any); ok {
		ci, _ := cu["items"].(map[string]any)
		c.compare(where+"[]", items, ci, request)
	}
	if ap, ok := b["additionalProperties"].(map[string]any); ok {
		cap, _ := cu["additionalProperties"].(map[string]any)
		c.compare(where+"{}", ap, cap, request)
	}
	bprops, _ := b["properties"].(map[string]any)
	cprops, _ := cu["properties"].(map[string]any)
	breq := map[string]bool{}
	for _, r := range toStrings(b["required"]) {
		breq[r] = true
	}
	creq := map[string]bool{}
	for _, r := range toStrings(cu["required"]) {
		creq[r] = true
	}
	for _, name := range mapKeys(bprops) {
		field := where + "." + name
		if bname != "" {
			field = bname + "." + name
		}
		cp, ok := cprops[name].(map[string]any)
		if !ok {
			c.add("%s: removed", field)
			continue
		}
		if !request && breq[name] && !creq[name] {
			c.add("%s: no longer always sent", field)
		}
		c.compare(field, bprops[name].(map[string]any), cp, request)
	}
	if request {
		for name := range creq {
			if !breq[name] {
				f := where + "." + name
				if bname != "" {
					f = bname + "." + name
				}
				c.add("%s: newly required", f)
			}
		}
	}
}
