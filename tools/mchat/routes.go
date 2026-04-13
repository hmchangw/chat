package main

import "net/http"

func (h *handler) registerRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /healthz", h.healthz)
	mux.HandleFunc("POST /api/login", h.login)
	mux.HandleFunc("POST /api/logout", h.logout)
	mux.HandleFunc("GET /api/rooms", h.listRooms)
	mux.HandleFunc("POST /api/rooms", h.createRoom)
	mux.HandleFunc("POST /api/rooms/{id}/invite", h.inviteMember)
	mux.HandleFunc("POST /api/rooms/{id}/messages", h.sendMessage)
	mux.HandleFunc("GET /api/events", h.events)
	mux.HandleFunc("GET /api/status", h.status)
	mux.HandleFunc("GET /", h.serveUI)
}
