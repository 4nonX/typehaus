package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

// Font identification: an image goes to the identification services an
// admin has entered a key for, and every match is looked up in the library
// and the free catalog, so the answer is "you have it", "import it" or "it's
// commercial". Services without an API (WhatTheFont, the Matcherators,
// Identifont) are offered as links in the UI, with the lookup to check the
// name they give.

var (
	whatFontIsURL = "https://www.whatfontis.com/api2/"
	mixfontURL    = "https://api.mixfont.com/v1/lens"
	asposeURL     = "https://api.aspose.cloud"
)

const (
	maxIdentifyImage = 10 << 20
	identifyPerHour  = 30
)

type identifyState struct {
	mu      sync.Mutex
	perUser map[int64][]time.Time
	images  map[string]tempImage
	aspose  struct {
		key   string // client id the token belongs to
		token string
		until time.Time
	}
}

type tempImage struct {
	data  []byte
	ctype string
	until time.Time
}

func newIdentifyState() *identifyState {
	return &identifyState{perUser: map[int64][]time.Time{}, images: map[string]tempImage{}}
}

// allow applies the per-user limit: the keys are the admin's, the quota is
// shared.
func (st *identifyState) allow(uid int64) bool {
	st.mu.Lock()
	defer st.mu.Unlock()
	cut := time.Now().Add(-time.Hour)
	var keep []time.Time
	for _, t := range st.perUser[uid] {
		if t.After(cut) {
			keep = append(keep, t)
		}
	}
	if len(keep) >= identifyPerHour {
		st.perUser[uid] = keep
		return false
	}
	st.perUser[uid] = append(keep, time.Now())
	return true
}

type libraryMatch struct {
	Family  string `json:"family"`
	Styles  int    `json:"styles"`
	FontID  int64  `json:"font_id"` // a regular style, for a preview
	License string `json:"license,omitempty"`
}

type identifyMatch struct {
	Name    string         `json:"name"`
	Score   float64        `json:"score,omitempty"` // 0–1 when the service gives one
	Free    *bool          `json:"free,omitempty"`  // WhatFontIs: free or commercial font
	URL     string         `json:"url,omitempty"`   // the font's page at the service
	Library *libraryMatch  `json:"library,omitempty"`
	Catalog *catalogFamily `json:"catalog,omitempty"`
}

type identifyServiceResult struct {
	Service string          `json:"service"`
	Name    string          `json:"name"`
	Status  string          `json:"status"` // ok, error, skipped
	Message string          `json:"message,omitempty"`
	Word    string          `json:"word,omitempty"` // the text the service looked at, if it says
	Style   string          `json:"style,omitempty"`
	Matches []identifyMatch `json:"matches"`
}

type identifyResponse struct {
	Results []identifyServiceResult `json:"results"`
}

type identifyInput struct {
	image    []byte
	ctype    string
	imageURL string
	freeOnly bool
	baseURL  string
}

func (s *Server) identifyOff(w http.ResponseWriter) bool {
	if !s.cfg.Identify {
		writeErrCode(w, 404, "feature_disabled", "font identification is switched off on this server (TYPEHAUS_IDENTIFY)")
		return true
	}
	return false
}

var imageTypes = map[string]bool{"image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true, "image/bmp": true}

func (s *Server) handleIdentify(w http.ResponseWriter, r *http.Request, u *authUser) {
	if s.identifyOff(w) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxIdentifyImage+1<<16)
	mr, err := r.MultipartReader()
	if err != nil {
		writeErr(w, 400, "expected multipart/form-data with an image or an image url")
		return
	}
	var in identifyInput
	var only map[string]bool
	for {
		p, err := mr.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			writeErrCode(w, 413, "payload_too_large", "the image is larger than 10 MB")
			return
		}
		switch p.FormName() {
		case "image":
			in.image, err = io.ReadAll(io.LimitReader(p, maxIdentifyImage+1))
			if err != nil || len(in.image) > maxIdentifyImage {
				writeErrCode(w, 413, "payload_too_large", "the image is larger than 10 MB")
				return
			}
		case "url":
			b, _ := io.ReadAll(io.LimitReader(p, 4096))
			in.imageURL = strings.TrimSpace(string(b))
		case "services":
			b, _ := io.ReadAll(io.LimitReader(p, 256))
			for _, x := range strings.Split(string(b), ",") {
				if x = strings.TrimSpace(x); x != "" {
					if only == nil {
						only = map[string]bool{}
					}
					only[x] = true
				}
			}
		case "free_only":
			b, _ := io.ReadAll(io.LimitReader(p, 8))
			in.freeOnly = string(b) == "1" || string(b) == "true"
		}
	}
	if len(in.image) > 0 {
		in.ctype = http.DetectContentType(in.image)
		if !imageTypes[in.ctype] {
			writeErr(w, 400, "the file isn't an image typehaus can send (PNG, JPEG, GIF, WebP or BMP)")
			return
		}
		in.imageURL = ""
	} else if in.imageURL != "" {
		pu, err := url.Parse(in.imageURL)
		if err != nil || (pu.Scheme != "http" && pu.Scheme != "https") || pu.Host == "" {
			writeErr(w, 400, "url must be an http(s) link to an image")
			return
		}
	} else {
		writeErr(w, 400, "send an image, or the url of one")
		return
	}
	var active []serviceDef
	for _, d := range services {
		if d.Kind == "identify" && s.serviceConfigured(d.ID) && (only == nil || only[d.ID]) {
			active = append(active, d)
		}
	}
	if len(active) == 0 {
		writeErrCode(w, 409, "not_configured", "no identification service has an API key yet – an administrator adds one under Integrations")
		return
	}
	if !s.identify.allow(u.ID) {
		w.Header().Set("Retry-After", "600")
		writeErrCode(w, 429, "rate_limited", fmt.Sprintf("at most %d identifications an hour per person", identifyPerHour))
		return
	}
	in.baseURL = s.cfg.BaseURL
	ctx, cancel := context.WithTimeout(r.Context(), 75*time.Second)
	defer cancel()
	results := make([]identifyServiceResult, len(active))
	var wg sync.WaitGroup
	for i, d := range active {
		wg.Add(1)
		go func() {
			defer wg.Done()
			res := identifyServiceResult{Service: d.ID, Name: d.Name, Status: "ok", Matches: []identifyMatch{}}
			var err error
			switch d.ID {
			case "whatfontis":
				err = s.identifyWhatFontIs(ctx, &in, &res)
			case "mixfont":
				err = s.identifyMixfont(ctx, &in, &res)
			case "aspose":
				err = s.identifyAspose(ctx, &in, &res)
			}
			var skip skipped
			if errors.As(err, &skip) {
				res.Status, res.Message = "skipped", string(skip)
			} else if err != nil {
				res.Status, res.Message = "error", err.Error()
				s.log.Warn("font identification failed", "service", d.ID, "err", err)
			}
			results[i] = res
		}()
	}
	wg.Wait()
	s.annotateMatches(r.Context(), u, results)
	var names []string
	for _, d := range active {
		names = append(names, d.Name)
	}
	s.audit(u, "font.identify", "%s", strings.Join(names, ", "))
	writeJSON(w, 200, identifyResponse{Results: results})
}

// skipped is an error meaning the service doesn't apply to this input.
type skipped string

func (s skipped) Error() string { return string(s) }

// serviceErr turns an upstream error response into a short message.
// checkReachable is privateAddress; tests whose stand-in services run on
// this machine replace it.
var checkReachable = privateAddress

// privateAddress reports why an outside service can't reach a URL's host:
// a name or address only meaningful inside a network. "" if it may be
// reachable (a public-looking name is given the benefit of the doubt).
func privateAddress(raw string) (string, string) {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return raw, "isn't a valid address"
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if ip := net.ParseIP(host); ip != nil {
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
			return host, "is a private network address"
		}
		return "", ""
	}
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return host, "is this computer only"
	}
	if !strings.Contains(host, ".") {
		return host, "is a local name without a domain"
	}
	for _, suf := range []string{".local", ".lan", ".internal", ".intranet", ".home", ".home.arpa", ".corp", ".localdomain", ".private", ".test", ".invalid", ".example"} {
		if strings.HasSuffix(host, suf) {
			return host, "only exists inside your network"
		}
	}
	return "", ""
}

func serviceErr(name string, resp *http.Response) error {
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	msg := strings.TrimSpace(string(b))
	ct := strings.ToLower(resp.Header.Get("Content-Type"))
	if strings.Contains(ct, "text/html") || strings.HasPrefix(strings.ToLower(msg), "<!doctype") || strings.HasPrefix(strings.ToLower(msg), "<html") {
		// a web page, not an API answer: say so instead of showing its source
		msg = "(answered with a web page instead of the API – the service may be down or its address may have changed; for Mixfont, it also happens when it can't fetch the image)"
	}
	var j map[string]any
	if json.Unmarshal(b, &j) == nil {
		for _, k := range []string{"message", "error", "detail"} {
			if v, ok := j[k].(string); ok && v != "" {
				msg = v
				break
			}
		}
	}
	if len(msg) > 300 {
		msg = msg[:300] + "…"
	}
	switch resp.StatusCode {
	case 401, 403:
		return fmt.Errorf("%s refused the API key (HTTP %d) %s", name, resp.StatusCode, msg)
	case 429:
		return fmt.Errorf("%s: the key's daily limit is used up", name)
	}
	return fmt.Errorf("%s: HTTP %d %s", name, resp.StatusCode, msg)
}

func (s *Server) identifyWhatFontIs(ctx context.Context, in *identifyInput, res *identifyServiceResult) error {
	key, _ := s.serviceValue("whatfontis", "key")
	form := url.Values{"API_KEY": {key}, "limit": {"10"}, "mode": {"pro"}, "NOTTEXTBOXSDETECTION": {"0"}}
	if in.freeOnly {
		form.Set("FREEFONTS", "1")
	} else {
		form.Set("FREEFONTS", "0")
	}
	if in.image != nil {
		form.Set("IMAGEBASE64", "1")
		form.Set("urlimagebase64", base64.StdEncoding.EncodeToString(in.image))
	} else {
		form.Set("IMAGEBASE64", "0")
		form.Set("urlimage", in.imageURL)
	}
	req, err := http.NewRequestWithContext(ctx, "POST", whatFontIsURL, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := s.outbound.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		if resp.StatusCode == 4223 || resp.StatusCode == 422 {
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
			return fmt.Errorf("WhatFontIs found no text in the image – crop it to one line of text (%s)", strings.TrimSpace(string(b)))
		}
		return serviceErr("WhatFontIs", resp)
	}
	var list []struct {
		Title string `json:"title"`
		URL   string `json:"url"`
		Type  string `json:"type"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&list); err != nil {
		return fmt.Errorf("WhatFontIs sent an answer typehaus doesn't understand")
	}
	for _, x := range list {
		if x.Title == "" {
			continue
		}
		m := identifyMatch{Name: x.Title, URL: x.URL}
		if x.Type != "" {
			free := strings.EqualFold(x.Type, "free")
			m.Free = &free
		}
		res.Matches = append(res.Matches, m)
	}
	return nil
}

func (s *Server) identifyMixfont(ctx context.Context, in *identifyInput, res *identifyServiceResult) error {
	key, _ := s.serviceValue("mixfont", "key")
	imgURL := in.imageURL
	if imgURL == "" {
		if in.baseURL == "" {
			return skipped("Mixfont needs a link to the image: paste an image link, or set TYPEHAUS_BASE_URL to an address Mixfont can reach")
		}
		if host, why := checkReachable(in.baseURL); why != "" {
			return skipped(fmt.Sprintf("Mixfont fetches the image itself, from the internet – and this server's address (%s) %s. Paste a link to an image on the web instead.", host, why))
		}
		tok := make([]byte, 24)
		rand.Read(tok)
		t := hex.EncodeToString(tok)
		s.identify.mu.Lock()
		for k, v := range s.identify.images {
			if time.Now().After(v.until) {
				delete(s.identify.images, k)
			}
		}
		s.identify.images[t] = tempImage{data: in.image, ctype: in.ctype, until: time.Now().Add(5 * time.Minute)}
		s.identify.mu.Unlock()
		defer func() {
			s.identify.mu.Lock()
			delete(s.identify.images, t)
			s.identify.mu.Unlock()
		}()
		imgURL = in.baseURL + "/identify-image/" + t
	}
	body, _ := json.Marshal(map[string]string{"image_url": imgURL})
	req, err := http.NewRequestWithContext(ctx, "POST", mixfontURL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", key)
	resp, err := s.outbound.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return serviceErr("Mixfont", resp)
	}
	var out struct {
		Word    *string `json:"word"`
		Matches []struct {
			Name  string  `json:"name"`
			Score float64 `json:"score"`
		} `json:"font_matches"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&out); err != nil {
		return fmt.Errorf("Mixfont sent an answer typehaus doesn't understand")
	}
	if out.Word != nil {
		res.Word = *out.Word
	}
	for _, m := range out.Matches {
		if m.Name != "" {
			res.Matches = append(res.Matches, identifyMatch{Name: m.Name, Score: m.Score})
		}
	}
	return nil
}

// handleIdentifyImage hands an uploaded image to Mixfont, which only takes
// links. The link is unguessable and lives for the duration of the request.
func (s *Server) handleIdentifyImage(w http.ResponseWriter, r *http.Request) {
	s.identify.mu.Lock()
	img, ok := s.identify.images[r.PathValue("token")]
	s.identify.mu.Unlock()
	if !ok || time.Now().After(img.until) {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", img.ctype)
	w.Header().Set("Cache-Control", "no-store")
	w.Write(img.data)
}

func (s *Server) asposeToken(ctx context.Context) (string, error) {
	id, _ := s.serviceValue("aspose", "client_id")
	secret, _ := s.serviceValue("aspose", "client_secret")
	st := s.identify
	st.mu.Lock()
	if st.aspose.key == id+secret && time.Now().Before(st.aspose.until) {
		t := st.aspose.token
		st.mu.Unlock()
		return t, nil
	}
	st.mu.Unlock()
	form := url.Values{"grant_type": {"client_credentials"}, "client_id": {id}, "client_secret": {secret}}
	req, err := http.NewRequestWithContext(ctx, "POST", asposeURL+"/connect/token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := s.outbound.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", serviceErr("Aspose", resp)
	}
	var tok struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&tok); err != nil || tok.AccessToken == "" {
		return "", errors.New("Aspose sent no access token")
	}
	life := time.Duration(max(tok.ExpiresIn, 120)-60) * time.Second
	st.mu.Lock()
	st.aspose.key, st.aspose.token, st.aspose.until = id+secret, tok.AccessToken, time.Now().Add(life)
	st.mu.Unlock()
	return tok.AccessToken, nil
}

func (s *Server) identifyAspose(ctx context.Context, in *identifyInput, res *identifyServiceResult) error {
	if in.image == nil {
		return skipped("Aspose needs the image itself – upload or paste it instead of a link")
	}
	tok, err := s.asposeToken(ctx)
	if err != nil {
		return err
	}
	body, _ := json.Marshal(map[string]any{
		"image": base64.StdEncoding.EncodeToString(in.image),
		"settings": map[string]any{"language": "English", "makeSkewCorrect": true, "makeContrastCorrection": true,
			"makeBinarization": false, "makeUpsampling": false, "rotate": 0, "resultType": "Text"},
	})
	do := func(method, u string, body io.Reader) ([]byte, error) {
		req, err := http.NewRequestWithContext(ctx, method, u, body)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "text/plain")
		resp, err := s.outbound.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			return nil, serviceErr("Aspose", resp)
		}
		return io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	}
	b, err := do("POST", asposeURL+"/v5.0/ocr/IdentifyFont", bytes.NewReader(body))
	if err != nil {
		return err
	}
	task := strings.Trim(strings.TrimSpace(string(b)), `"`)
	if task == "" {
		return errors.New("Aspose sent no task id")
	}
	for {
		b, err := do("GET", asposeURL+"/v5.0/ocr/IdentifyFont?id="+url.QueryEscape(task), nil)
		if err != nil {
			return err
		}
		var out struct {
			Status  string `json:"taskStatus"`
			Results []struct {
				Data string `json:"data"`
			} `json:"results"`
			Error *struct {
				Messages []string `json:"messages"`
			} `json:"error"`
		}
		if err := json.Unmarshal(b, &out); err != nil {
			return errors.New("Aspose sent an answer typehaus doesn't understand")
		}
		switch out.Status {
		case "Completed":
			for _, r := range out.Results {
				style, fonts := parseAsposeFonts(r.Data)
				res.Style = style
				for _, f := range fonts {
					res.Matches = append(res.Matches, identifyMatch{Name: f})
				}
			}
			return nil
		case "Error", "NotExist":
			msg := "Aspose couldn't identify the font"
			if out.Error != nil && len(out.Error.Messages) > 0 {
				msg += ": " + strings.Join(out.Error.Messages, "; ")
			}
			return errors.New(msg)
		}
		select {
		case <-ctx.Done():
			return errors.New("Aspose took too long")
		case <-time.After(1500 * time.Millisecond):
		}
	}
}

// parseAsposeFonts reads Aspose's result, a Base64 encoded Python-style
// dict: {'style': 'regular', 'font': ['tahoma', 'times']}.
func parseAsposeFonts(data string) (string, []string) {
	raw, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return "", nil
	}
	var v struct {
		Style string   `json:"style"`
		Font  []string `json:"font"`
	}
	if json.Unmarshal(raw, &v) != nil {
		json.Unmarshal([]byte(strings.ReplaceAll(string(raw), "'", `"`)), &v)
	}
	var fonts []string
	for _, f := range v.Font {
		if f = strings.TrimSpace(f); f != "" {
			fonts = append(fonts, titleCase(f))
		}
	}
	return v.Style, fonts
}

func titleCase(s string) string {
	if strings.ToLower(s) != s {
		return s
	}
	w := strings.Fields(s)
	for i, x := range w {
		w[i] = strings.ToUpper(x[:1]) + x[1:]
	}
	return strings.Join(w, " ")
}

// ---- matching against the library and the catalog --------------------------

type libraryIndex struct {
	families map[string]*libraryMatch // family key → family
	names    map[string]*libraryMatch // full/PostScript name key → family
}

func (s *Server) libraryIndex(u *authUser) libraryIndex {
	idx := libraryIndex{families: map[string]*libraryMatch{}, names: map[string]*libraryMatch{}}
	where, args := visibleClause(u)
	rows, err := s.db.Query(`SELECT f.id, f.family, f.full_name, f.postscript_name, f.weight, f.italic, COALESCE(l.name, '')
		FROM fonts f LEFT JOIN licenses l ON l.id = f.license_id WHERE `+where+` ORDER BY f.id`, args...)
	if err != nil {
		return idx
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var fam, full, ps, lic string
		var weight int
		var italic bool
		rows.Scan(&id, &fam, &full, &ps, &weight, &italic, &lic)
		k := familyKey(fam)
		if k == "" {
			continue
		}
		m := idx.families[k]
		if m == nil {
			m = &libraryMatch{Family: fam, FontID: id, License: lic}
			idx.families[k] = m
		}
		m.Styles++
		if !italic && weight == 400 {
			m.FontID = id
		}
		if m.License == "" {
			m.License = lic
		}
		idx.names[familyKey(full)] = m
		idx.names[familyKey(ps)] = m
	}
	return idx
}

// find matches a name like "Helvetica Neue LT Pro 55 Roman": the exact
// font name, else the longest leading words that name a family.
func (idx libraryIndex) find(name string) *libraryMatch {
	if m := idx.names[familyKey(name)]; m != nil {
		return m
	}
	w := strings.Fields(name)
	for n := len(w); n > 0; n-- {
		if m := idx.families[familyKey(strings.Join(w[:n], " "))]; m != nil {
			return m
		}
	}
	return nil
}

func (s *Server) catalogByFamily(ctx context.Context) map[string]*catalogEntry {
	if !s.cfg.Catalog {
		return nil
	}
	entries, err := s.catalogIndex(ctx)
	if err != nil {
		return nil
	}
	out := make(map[string]*catalogEntry, len(entries))
	for _, e := range entries {
		out[familyKey(e.Family)] = e
	}
	return out
}

func findCatalog(cat map[string]*catalogEntry, name string) *catalogEntry {
	w := strings.Fields(name)
	for n := len(w); n > 0; n-- {
		if e := cat[familyKey(strings.Join(w[:n], " "))]; e != nil {
			return e
		}
	}
	return nil
}

func (s *Server) annotateMatches(ctx context.Context, u *authUser, results []identifyServiceResult) {
	lib := s.libraryIndex(u)
	cat := s.catalogByFamily(ctx)
	keys, refs := s.libraryFamilies()
	for i := range results {
		for j := range results[i].Matches {
			m := &results[i].Matches[j]
			m.Library = lib.find(m.Name)
			if e := findCatalog(cat, m.Name); e != nil {
				f := e.result(keys, refs)
				m.Catalog = &f
			}
		}
	}
}

type lookupResponse struct {
	Name    string          `json:"name"`
	Library []libraryMatch  `json:"library"`
	Catalog []catalogFamily `json:"catalog"`
}

// handleLookup checks a font name, e.g. one WhatTheFont gave: is it in the
// library, under which license, or can it be imported from the catalog?
func (s *Server) handleLookup(w http.ResponseWriter, r *http.Request, u *authUser) {
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	if name == "" {
		writeErr(w, 400, "name required")
		return
	}
	out := lookupResponse{Name: name, Library: []libraryMatch{}, Catalog: []catalogFamily{}}
	lib := s.libraryIndex(u)
	seen := map[*libraryMatch]bool{}
	if m := lib.find(name); m != nil {
		out.Library = append(out.Library, *m)
		seen[m] = true
	}
	q := familyKey(name)
	var more []*libraryMatch
	for k, m := range lib.families {
		if !seen[m] && len(q) >= 3 && strings.Contains(k, q) {
			more = append(more, m)
			seen[m] = true
		}
	}
	sort.Slice(more, func(i, j int) bool { return more[i].Family < more[j].Family })
	for _, m := range more {
		if len(out.Library) >= 10 {
			break
		}
		out.Library = append(out.Library, *m)
	}
	if cat := s.catalogByFamily(r.Context()); cat != nil {
		keys, refs := s.libraryFamilies()
		if e := findCatalog(cat, name); e != nil {
			out.Catalog = append(out.Catalog, e.result(keys, refs))
		}
		var extra []catalogFamily
		for k, e := range cat {
			if len(q) >= 3 && strings.Contains(k, q) && (len(out.Catalog) == 0 || out.Catalog[0].ID != e.ID) {
				extra = append(extra, e.result(keys, refs))
			}
		}
		sort.Slice(extra, func(i, j int) bool { return len(extra[i].Family) < len(extra[j].Family) })
		for _, e := range extra {
			if len(out.Catalog) >= 10 {
				break
			}
			out.Catalog = append(out.Catalog, e)
		}
	}
	writeJSON(w, 200, out)
}

// identifyLinks are the identification sites without an API.
type identifyLink struct {
	Name  string `json:"name"`
	URL   string `json:"url"`
	About string `json:"about"`
}

var identifyLinks = []identifyLink{
	{"WhatTheFont", "https://www.myfonts.com/pages/whatthefont", "MyFonts' identifier; matches commercial fonts sold on MyFonts."},
	{"Fontspring Matcherator", "https://www.fontspring.com/matcherator", "Strong on OpenType features and independent foundries."},
	{"Font Squirrel Matcherator", "https://www.fontsquirrel.com/matcherator", "The same engine, limited to free fonts."},
	{"WhatFontIs", "https://www.whatfontis.com/", "The largest database, free and commercial."},
	{"Mixfont Font Finder", "https://www.mixfont.com/font-finder", "Finds the closest open-source font."},
	{"Identifont", "https://www.identifont.com/", "Identifies a font from answers about its letter shapes, without an image."},
}

type identifyInfo struct {
	Enabled  bool           `json:"enabled"`
	Services []string       `json:"services"` // configured services
	Links    []identifyLink `json:"links"`
	Catalog  bool           `json:"catalog"`
	BaseURL  bool           `json:"base_url"` // uploads can reach link-only services
}

func (s *Server) handleIdentifyInfo(w http.ResponseWriter, r *http.Request, u *authUser) {
	info := identifyInfo{Enabled: s.cfg.Identify, Services: []string{}, Links: identifyLinks, Catalog: s.cfg.Catalog, BaseURL: s.cfg.BaseURL != ""}
	if s.cfg.Identify {
		for _, d := range services {
			if d.Kind == "identify" && s.serviceConfigured(d.ID) {
				info.Services = append(info.Services, d.ID)
			}
		}
	}
	writeJSON(w, 200, info)
}
