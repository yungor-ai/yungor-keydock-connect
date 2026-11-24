package desktop

import (
	"embed"
	"net/http"
)

//go:embed ui/*
var ui embed.FS

func (a *App) servePage(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" && r.Method != "HEAD" {
		w.WriteHeader(405)
		return
	}
	var name, mime string
	switch r.URL.Path {
	case "/":
		name, mime = "index.html", "text/html; charset=utf-8"
	case "/desk.css":
		name, mime = "desk.css", "text/css; charset=utf-8"
	case "/desk.js":
		name, mime = "desk.js", "text/javascript; charset=utf-8"
	default:
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
	w.Header().Set("Content-Type", mime)
	data, _ := ui.ReadFile("ui/" + name)
	if r.Method == "GET" {
		_, _ = w.Write(data)
	}
}
