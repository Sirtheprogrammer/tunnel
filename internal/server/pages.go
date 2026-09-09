package server

import (
	"fmt"
	"html/template"
	"net/http"
	"strings"
)

// errorPage describes a branded failure page. These are the most-seen surface
// of the service when something is wrong, so they say what happened and what to
// do about it rather than showing a bare status code.
type errorPage struct {
	Title  string
	Detail string
	Hint   string
}

var (
	pageTunnelNotFound = errorPage{
		Title:  "Tunnel not found",
		Detail: "No agent is currently serving this address.",
		Hint:   "The tunnel may have been closed, or expired when the agent disconnected. Start it again with tunnelx http <port>.",
	}
	pageNoSuchHost = errorPage{
		Title:  "Unknown address",
		Detail: "This hostname is not a tunnel address.",
		Hint:   "Tunnel URLs look like https://brave-otter-7f3a.tl.codesky.tech.",
	}
	pageAgentUnreachable = errorPage{
		Title:  "Agent unreachable",
		Detail: "The tunnel is registered, but the local service behind it did not respond.",
		Hint:   "Check that your local server is still running and listening on the forwarded port.",
	}
	pageInternal = errorPage{
		Title:  "Something went wrong",
		Detail: "The tunnel server hit an unexpected error handling this request.",
		Hint:   "If this keeps happening, please report it.",
	}
)

var errorPageTmpl = template.Must(template.New("error").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>{{.Status}} &middot; {{.Title}}</title>
<style>
  :root { color-scheme: light dark; }
  body { margin:0; min-height:100vh; display:grid; place-items:center;
         font:16px/1.6 ui-sans-serif,system-ui,-apple-system,"Segoe UI",sans-serif;
         background:#fafafa; color:#18181b; }
  @media (prefers-color-scheme: dark) { body { background:#09090b; color:#e4e4e7; } }
  main { max-width:34rem; padding:2rem; }
  .status { font-size:.75rem; letter-spacing:.08em; text-transform:uppercase;
            color:#71717a; margin:0 0 .5rem; }
  h1 { font-size:1.5rem; margin:0 0 .75rem; font-weight:600; }
  p { margin:0 0 1rem; }
  .hint { color:#71717a; font-size:.9375rem; }
  code { font-family:ui-monospace,SFMono-Regular,Menlo,monospace; font-size:.9em;
         background:rgba(128,128,128,.15); padding:.15em .4em; border-radius:.25rem; }
  footer { margin-top:2rem; font-size:.8125rem; color:#a1a1aa; }
</style>
</head>
<body>
<main>
  <p class="status">Error {{.Status}}</p>
  <h1>{{.Title}}</h1>
  <p>{{.Detail}}</p>
  <p class="hint">{{.Hint}}</p>
  <footer>tunnelx</footer>
</main>
</body>
</html>
`))

// writeErrorPage renders a branded page, or a plain-text equivalent when the
// caller is a CLI or API client rather than a browser.
func (s *Server) writeErrorPage(w http.ResponseWriter, r *http.Request, status int, page errorPage) {
	h := w.Header()
	h.Del("Content-Length")
	h.Set("Cache-Control", "no-store")

	if !strings.Contains(r.Header.Get("Accept"), "text/html") {
		h.Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(status)
		fmt.Fprintf(w, "%s\n\n%s\n%s\n", page.Title, page.Detail, page.Hint)
		return
	}

	h.Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	data := struct {
		errorPage
		Status int
	}{page, status}
	if err := errorPageTmpl.Execute(w, data); err != nil {
		s.log.Warn("render error page", "error", err)
	}
}
