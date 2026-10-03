// komga-ereader-page: a tiny server-rendered search/download front end for
// Komga, designed for e-ink browsers (no JS, no external assets).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	pageSize     = 20
	apiTimeout   = 10 * time.Second
	maxQueryLen  = 200
	maxJSONBytes = 4 << 20
)

// Komga book IDs are short alphanumeric strings (ULID-like; older versions
// used 32 hex chars). Anything else is rejected before touching upstream.
var bookIDRe = regexp.MustCompile(`^[A-Za-z0-9]{8,40}$`)

type app struct {
	baseURL string
	apiKey  string
	api     *http.Client // JSON calls, with overall timeout
	dl      *http.Client // file streaming: header timeout only
}

// Subset of Komga's BookDto / PageBookDto (verified against /v3/api-docs).
type book struct {
	ID          string `json:"id"`
	SeriesTitle string `json:"seriesTitle"`
	Size        string `json:"size"`
	URL         string `json:"url"`
	Media       struct {
		MediaType string `json:"mediaType"`
	} `json:"media"`
	Metadata struct {
		Title string `json:"title"`
	} `json:"metadata"`
	Name string `json:"name"`
}

type bookPage struct {
	Content       []book `json:"content"`
	Number        int    `json:"number"`
	TotalPages    int    `json:"totalPages"`
	TotalElements int    `json:"totalElements"`
	Last          bool   `json:"last"`
}

type row struct {
	ID, Title, Series, Format, Size string
}

type view struct {
	Query       string
	Searched    bool
	Rows        []row
	Total       int
	Page, Pages int
	PrevURL     string
	NextURL     string
	Error       string
}

var tpl = template.Must(template.New("p").Parse(`<!DOCTYPE html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Library</title>
<style>
body{background:#fff;color:#000;font-family:serif;font-size:22px;line-height:1.4;margin:0;padding:12px}
h1{font-size:30px;margin:0 0 12px}
form{margin:0 0 16px}
input[type=text]{font-size:26px;width:100%;box-sizing:border-box;padding:14px;border:3px solid #000;background:#fff;color:#000}
button,a.btn{display:block;box-sizing:border-box;width:100%;font-size:26px;font-family:inherit;padding:16px;margin-top:10px;border:3px solid #000;background:#fff;color:#000;text-align:center;text-decoration:none}
button{font-weight:bold}
ul{list-style:none;margin:0;padding:0}
li{border-top:2px solid #000;padding:14px 0}
.t{font-weight:bold;font-size:24px}
.m{margin:4px 0 0}
.nav{margin-top:16px}
.err{border:3px solid #000;padding:14px;font-weight:bold}
a{color:#000}
</style></head><body>
<h1>Library</h1>
<form method="get" action="/">
<input type="text" name="q" value="{{.Query}}" placeholder="Title, author, series" autofocus>
<button type="submit">Search</button>
</form>
{{if .Error}}<p class="err">{{.Error}}</p>{{end}}
{{if .Searched}}{{if not .Error}}
<p>{{.Total}} result(s){{if gt .Pages 1}}, page {{.Page}} of {{.Pages}}{{end}}</p>
<ul>
{{range .Rows}}<li>
<div class="t">{{.Title}}</div>
{{if .Series}}<div class="m">Series: {{.Series}}</div>{{end}}
<div class="m">{{.Format}}{{if .Size}} &middot; {{.Size}}{{end}}</div>
<a class="btn" href="/download/{{.ID}}">Download</a>
</li>{{end}}
</ul>
<div class="nav">
{{if .PrevURL}}<a class="btn" href="{{.PrevURL}}">&laquo; Previous</a>{{end}}
{{if .NextURL}}<a class="btn" href="{{.NextURL}}">Next &raquo;</a>{{end}}
</div>
{{end}}{{end}}
</body></html>
`))

func main() {
	base := strings.TrimRight(os.Getenv("KOMGA_URL"), "/")
	key := os.Getenv("KOMGA_API_KEY")
	if base == "" || key == "" {
		log.Fatal("KOMGA_URL and KOMGA_API_KEY must be set")
	}
	if u, err := url.Parse(base); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		log.Fatal("KOMGA_URL must be a valid http(s) URL")
	}
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	tr := &http.Transport{
		DialContext:           (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: apiTimeout,
		MaxIdleConns:          4,
		IdleConnTimeout:       60 * time.Second,
	}
	a := &app{
		baseURL: base,
		apiKey:  key,
		api:     &http.Client{Transport: tr, Timeout: apiTimeout},
		dl:      &http.Client{Transport: tr}, // no total timeout: large files
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", a.index)
	mux.HandleFunc("GET /download/{id}", a.download)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { io.WriteString(w, "ok") })

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	log.Printf("listening on :%s, upstream %s", port, base)
	log.Fatal(srv.ListenAndServe())
}

// get performs an authenticated GET against Komga. The key never leaves this function.
func (a *app) get(ctx context.Context, c *http.Client, p string, q url.Values) (*http.Response, error) {
	u := a.baseURL + p
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-API-Key", a.apiKey)
	return c.Do(req)
}

func (a *app) getJSON(ctx context.Context, p string, q url.Values, out any) error {
	ctx, cancel := context.WithTimeout(ctx, apiTimeout)
	defer cancel()
	resp, err := a.get(ctx, a.api, p, q)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		return &statusError{resp.StatusCode}
	}
	return json.NewDecoder(io.LimitReader(resp.Body, maxJSONBytes)).Decode(out)
}

type statusError struct{ code int }

func (e *statusError) Error() string { return "komga returned HTTP " + strconv.Itoa(e.code) }

func friendly(err error) string {
	var se *statusError
	if errors.As(err, &se) {
		switch se.code {
		case 401, 403:
			return "The server is not authorised to access the library (check the API key)."
		case 404:
			return "Not found."
		}
	}
	return "The library server is not reachable right now. Please try again later."
}

func (a *app) index(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if len(q) > maxQueryLen {
		q = q[:maxQueryLen]
	}
	v := view{Query: q}
	if q != "" {
		v.Searched = true
		pg, _ := strconv.Atoi(r.URL.Query().Get("page"))
		if pg < 0 || pg > 100000 {
			pg = 0
		}
		var res bookPage
		err := a.getJSON(r.Context(), "/api/v1/books", url.Values{
			"search": {q},
			"size":   {strconv.Itoa(pageSize)},
			"page":   {strconv.Itoa(pg)},
		}, &res)
		if err != nil {
			log.Printf("search failed: %v", err)
			v.Error = friendly(err)
			render(w, v, http.StatusBadGateway)
			return
		}
		for _, b := range res.Content {
			v.Rows = append(v.Rows, row{
				ID:     b.ID,
				Title:  firstNonEmpty(b.Metadata.Title, b.Name, b.ID),
				Series: b.SeriesTitle,
				Format: formatLabel(b),
				Size:   b.Size,
			})
		}
		v.Total, v.Page, v.Pages = res.TotalElements, res.Number+1, res.TotalPages
		link := func(n int) string {
			return "/?" + url.Values{"q": {q}, "page": {strconv.Itoa(n)}}.Encode()
		}
		if res.Number > 0 {
			v.PrevURL = link(res.Number - 1)
		}
		if !res.Last && len(res.Content) > 0 {
			v.NextURL = link(res.Number + 1)
		}
	}
	render(w, v, http.StatusOK)
}

func render(w http.ResponseWriter, v view, status int) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	if err := tpl.Execute(w, v); err != nil {
		log.Printf("render: %v", err)
	}
}

func (a *app) download(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !bookIDRe.MatchString(id) {
		http.Error(w, "Invalid book id", http.StatusBadRequest)
		return
	}
	var b book
	if err := a.getJSON(r.Context(), "/api/v1/books/"+id, nil, &b); err != nil {
		log.Printf("book lookup failed: %v", err)
		errPage(w, err)
		return
	}
	resp, err := a.get(r.Context(), a.dl, "/api/v1/books/"+id+"/file", nil)
	if err != nil {
		log.Printf("file fetch failed: %v", err)
		errPage(w, err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		errPage(w, &statusError{resp.StatusCode})
		return
	}

	ext := strings.ToLower(path.Ext(strings.ReplaceAll(b.URL, `\`, "/")))
	title := firstNonEmpty(b.Metadata.Title, b.Name, id)
	w.Header().Set("Content-Type", contentType(ext, b.Media.MediaType))
	w.Header().Set("Content-Disposition", contentDisposition(sanitizeFilename(title)+safeExt(ext)))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if cl := resp.Header.Get("Content-Length"); cl != "" {
		w.Header().Set("Content-Length", cl)
	}
	if _, err := io.Copy(w, resp.Body); err != nil {
		log.Printf("download %s interrupted: %v", id, err)
	}
}

func errPage(w http.ResponseWriter, err error) {
	status := http.StatusBadGateway
	var se *statusError
	if errors.As(err, &se) && se.code == 404 {
		status = http.StatusNotFound
	}
	render(w, view{Error: friendly(err)}, status)
}

func firstNonEmpty(s ...string) string {
	for _, x := range s {
		if strings.TrimSpace(x) != "" {
			return strings.TrimSpace(x)
		}
	}
	return ""
}

func formatLabel(b book) string {
	if e := strings.TrimPrefix(strings.ToUpper(path.Ext(strings.ReplaceAll(b.URL, `\`, "/"))), "."); e != "" {
		return e
	}
	if t := b.Media.MediaType; t != "" {
		return t
	}
	return "file"
}

var knownTypes = map[string]string{
	".epub":  "application/epub+zip",
	".kepub": "application/epub+zip",
	".pdf":   "application/pdf",
	".cbz":   "application/vnd.comicbook+zip",
	".cbr":   "application/vnd.comicbook-rar",
	".zip":   "application/zip",
	".rar":   "application/vnd.rar",
	".7z":    "application/x-7z-compressed",
}

func contentType(ext, mediaType string) string {
	if t, ok := knownTypes[ext]; ok {
		return t
	}
	if mt, _, err := mime.ParseMediaType(mediaType); err == nil && mt != "" {
		return mt
	}
	return "application/octet-stream"
}

var (
	unsafeChars = regexp.MustCompile(`[\x00-\x1f\x7f"*/:<>?\\|;%]+`)
	spaces      = regexp.MustCompile(`\s+`)
	extRe       = regexp.MustCompile(`^\.[a-z0-9]{1,8}$`)
)

func safeExt(ext string) string {
	if extRe.MatchString(ext) {
		return ext
	}
	return ""
}

// sanitizeFilename strips path separators, control and shell-special chars,
// collapses whitespace and bounds the length (in runes).
func sanitizeFilename(s string) string {
	s = unsafeChars.ReplaceAllString(s, " ")
	s = strings.Trim(spaces.ReplaceAllString(s, " "), " .")
	if r := []rune(s); len(r) > 100 {
		s = strings.TrimSpace(string(r[:100]))
	}
	if s == "" {
		s = "book"
	}
	return s
}

// contentDisposition emits an ASCII fallback plus an RFC 5987 UTF-8 filename.
func contentDisposition(name string) string {
	ascii := strings.Map(func(r rune) rune {
		if r < 0x20 || r > 0x7e {
			return '_'
		}
		return r
	}, name)
	return fmt.Sprintf(`attachment; filename="%s"; filename*=UTF-8''%s`,
		ascii, strings.ReplaceAll(url.QueryEscape(name), "+", "%20"))
}
