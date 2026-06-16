package main

import (
	_ "embed"
	"net/http"
)

//go:embed static/index.html
var indexHTML []byte

func (h *handler) serveUI(w http.ResponseWriter, r *http.Request) {
	h.sessions.resolve(w, r) // establish the session cookie at page load
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	w.Write(indexHTML) //nolint:errcheck
}
