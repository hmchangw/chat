package handler

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/google/uuid"
	"github.com/hmchangw/chat/internal/model"
	"github.com/hmchangw/chat/internal/repository"
)

type MessageHandler struct {
	repo *repository.MessageRepository
}

func NewMessageHandler(repo *repository.MessageRepository) *MessageHandler {
	return &MessageHandler{repo: repo}
}

func (h *MessageHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Route: /messages and /messages/{id}
	path := strings.TrimPrefix(r.URL.Path, "/messages")
	path = strings.Trim(path, "/")

	if path == "" {
		switch r.Method {
		case http.MethodGet:
			h.list(w, r)
		case http.MethodPost:
			h.create(w, r)
		default:
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		}
		return
	}

	id, err := uuid.Parse(path)
	if err != nil {
		http.Error(w, "Invalid message ID", http.StatusBadRequest)
		return
	}

	switch r.Method {
	case http.MethodGet:
		h.getByID(w, r, id)
	case http.MethodPut:
		h.update(w, r, id)
	case http.MethodDelete:
		h.delete(w, r, id)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (h *MessageHandler) list(w http.ResponseWriter, r *http.Request) {
	messages, err := h.repo.List()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if messages == nil {
		messages = []*model.Message{}
	}

	writeJSON(w, http.StatusOK, messages)
}

func (h *MessageHandler) create(w http.ResponseWriter, r *http.Request) {
	var req model.CreateMessageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if req.Sender == "" || req.Content == "" {
		http.Error(w, "sender and content are required", http.StatusBadRequest)
		return
	}

	msg, err := h.repo.Create(req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	writeJSON(w, http.StatusCreated, msg)
}

func (h *MessageHandler) getByID(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	msg, err := h.repo.GetByID(id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if msg == nil {
		http.Error(w, "Message not found", http.StatusNotFound)
		return
	}

	writeJSON(w, http.StatusOK, msg)
}

func (h *MessageHandler) update(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	var req model.UpdateMessageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request body", http.StatusBadRequest)
		return
	}

	if req.Content == "" {
		http.Error(w, "content is required", http.StatusBadRequest)
		return
	}

	msg, err := h.repo.Update(id, req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if msg == nil {
		http.Error(w, "Message not found", http.StatusNotFound)
		return
	}

	writeJSON(w, http.StatusOK, msg)
}

func (h *MessageHandler) delete(w http.ResponseWriter, r *http.Request, id uuid.UUID) {
	if err := h.repo.Delete(id); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
