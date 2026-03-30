package main

import "net/http"

func (h *handler) registerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /healthz", h.healthz)
	mux.HandleFunc("POST /api/connect", h.connect)
	mux.HandleFunc("POST /api/disconnect", h.disconnect)
	mux.HandleFunc("GET /api/status", h.status)
	mux.HandleFunc("POST /api/subscriptions", h.subscribe)
	mux.HandleFunc("DELETE /api/subscriptions/{id}", h.unsubscribe)
	mux.HandleFunc("GET /api/subscriptions", h.listSubscriptions)
	mux.HandleFunc("POST /api/publish", h.publish)
	mux.HandleFunc("GET /api/events", h.events)
	mux.HandleFunc("GET /", h.serveUI)
}
