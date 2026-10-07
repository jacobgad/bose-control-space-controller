// Package webui is the add-on's ingress page: it shows which design is running and
// accepts a new ControlSpace Designer .csp, which replaces the running design.
package webui

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/jacobgad/bose-control-space-controller/internal/design"
)

// Status is what the page reports about the running bridge.
type Status struct {
	Design   *design.Design
	File     string
	LoadedAt time.Time
	// nil whenever Design is set
	Err error
}

// Deps connect the page to the application.
type Deps struct {
	Status        func() Status
	Upload        func(name string, data []byte) error
	Log           *slog.Logger
	AllowedRemote string
}

// MaxUpload bounds a request body; real project files are under 1 MiB.
const MaxUpload = 32 << 20

// IngressProxy is the Supervisor's address on the add-on network, the only client
// that should reach an ingress page.
const IngressProxy = "172.30.32.2"

// Handler serves the page at "/" and uploads at "/upload", all with relative URLs
// so it works under the ingress path prefix.
func Handler(deps Deps) http.Handler {
	if deps.Log == nil {
		deps.Log = slog.Default()
	}
	s := &server{deps: deps}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.index)
	mux.HandleFunc("POST /upload", s.upload)
	if deps.AllowedRemote == "" {
		return mux
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil || host != deps.AllowedRemote {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

// Serve runs h on addr until ctx is cancelled.
func Serve(ctx context.Context, addr string, h http.Handler, log *slog.Logger) error {
	srv := &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       2 * time.Minute,
		WriteTimeout:      2 * time.Minute,
	}
	errc := make(chan error, 1)
	go func() {
		log.Info("webui_listening", "addr", addr)
		errc <- srv.ListenAndServe()
	}()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	case err := <-errc:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

type server struct {
	deps Deps
}

type pageData struct {
	Status
	Error string
	Saved bool
}

func (s *server) index(w http.ResponseWriter, r *http.Request) {
	s.render(w, http.StatusOK, pageData{Status: s.deps.Status(), Saved: r.URL.Query().Has("saved")})
}

func (s *server) upload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, MaxUpload)
	file, header, err := r.FormFile("design")
	if err != nil {
		s.fail(w, http.StatusBadRequest, fmt.Errorf("choose a .csp file to upload: %w", err))
		return
	}
	defer file.Close()
	data, err := io.ReadAll(file)
	if err != nil {
		s.fail(w, http.StatusBadRequest, err)
		return
	}
	if err := s.deps.Upload(header.Filename, data); err != nil {
		s.deps.Log.Warn("design_upload_rejected", "file", header.Filename, "error", err.Error())
		s.fail(w, http.StatusUnprocessableEntity, err)
		return
	}
	s.deps.Log.Info("design_uploaded", "file", header.Filename, "bytes", len(data))
	// http.Redirect would resolve this to an absolute path, which escapes the ingress prefix.
	w.Header().Set("Location", "./?saved")
	w.WriteHeader(http.StatusSeeOther)
}

func (s *server) fail(w http.ResponseWriter, code int, err error) {
	s.render(w, code, pageData{Status: s.deps.Status(), Error: err.Error()})
}

func (s *server) render(w http.ResponseWriter, code int, data pageData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	if err := page.Execute(w, data); err != nil {
		s.deps.Log.Warn("webui_render_failed", "error", err.Error())
	}
}

var page = template.Must(template.New("page").Funcs(template.FuncMap{
	"when": func(t time.Time) string { return t.Local().Format("2 Jan 2006 15:04:05") },
}).Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Bose ControlSpace Controller</title>
<style>
body{font-family:system-ui,-apple-system,sans-serif;margin:0;padding:24px;background:#fafafa;color:#212121}
main{max-width:760px;margin:0 auto}
h1{font-size:1.4rem;margin:0 0 16px}
h2{font-size:1.05rem;margin:0 0 12px}
section{background:#fff;border-radius:12px;box-shadow:0 1px 3px rgba(0,0,0,.2);padding:16px 24px;margin-bottom:16px}
dl{display:grid;grid-template-columns:max-content 1fr;gap:6px 20px;margin:0 0 12px}
dt{color:#727272}dd{margin:0}
table{border-collapse:collapse;width:100%}
th,td{text-align:left;padding:6px 8px;border-bottom:1px solid #eee;font-size:.95rem}
th{color:#727272;font-weight:500}
code{font-size:.9em;background:#f1f1f1;padding:1px 4px;border-radius:3px}
.notice{padding:12px 16px;border-radius:8px;margin-bottom:16px}
.error{background:#fdecea;color:#b71c1c}
.ok{background:#e8f5e9;color:#1b5e20}
.muted{color:#727272}
input[type=file]{display:block;margin:12px 0}
button{background:#03a9f4;color:#fff;border:0;border-radius:4px;padding:10px 18px;font-size:.95rem;cursor:pointer}
button:hover{background:#0288d1}
</style>
</head>
<body>
<main>
<h1>Bose ControlSpace Controller</h1>
{{if .Saved}}<div class="notice ok">Design saved and applied.</div>{{end}}
{{if .Error}}<div class="notice error">{{.Error}}</div>{{end}}
<section>
<h2>Running design</h2>
{{if .Design}}
<dl>
<dt>File</dt><dd><code>{{.File}}</code></dd>
<dt>Designer</dt><dd>{{.Design.CreatedBy}}</dd>
<dt>Loaded</dt><dd>{{when .LoadedAt}}</dd>
<dt>Blocks</dt><dd>{{len .Design.Blocks}}{{if .Design.Skipped}} <span class="muted">({{len .Design.Skipped}} skipped, see log)</span>{{end}}</dd>
<dt>Parameter sets</dt><dd>{{len .Design.ParameterSets}}</dd>
</dl>
<table>
<thead><tr><th>Device</th><th>Model</th><th>Address</th><th>Firmware</th></tr></thead>
<tbody>
{{range .Design.Devices}}<tr><td>{{.Label}}{{if .IsMain}} <span class="muted">(main)</span>{{end}}</td><td>{{.Model}}</td><td><code>{{.Address}}</code></td><td>{{.Firmware}}</td></tr>
{{end}}</tbody>
</table>
{{else}}
<p>No design is running.{{if .Err}} <span class="muted">{{.Err}}</span>{{end}}</p>
<p>Upload the ControlSpace Designer project file for this installation to start the bridge.</p>
{{end}}
</section>
<section>
<h2>Upload a design</h2>
<p class="muted">Choose a <code>.csp</code> saved by ControlSpace Designer. It is checked before anything changes; if it is valid the bridge restarts with it, and entities for blocks that no longer exist are removed from Home Assistant.</p>
<form method="post" action="upload" enctype="multipart/form-data">
<input type="file" name="design" accept=".csp" required>
<button type="submit">Upload and apply</button>
</form>
</section>
</main>
</body>
</html>
`))
