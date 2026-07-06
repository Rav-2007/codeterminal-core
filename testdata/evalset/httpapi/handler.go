// Package httpapi implements the HTTP handlers for the widgets resource.
package httpapi

import (
	"encoding/json"
	"net/http"
)

// Widget is the resource this handler creates and returns.
type Widget struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Color string `json:"color"`
}

// Store is the minimal persistence interface this handler depends on.
type Store interface {
	CreateWidget(name, color string) (Widget, error)
}

// Handler serves the widgets HTTP API.
type Handler struct {
	store Store
}

// NewHandler returns a Handler backed by store.
func NewHandler(store Store) *Handler {
	return &Handler{store: store}
}

// createWidgetRequest is the expected JSON body for CreateWidget.
type createWidgetRequest struct {
	Name  string `json:"name"`
	Color string `json:"color"`
}

// CreateWidget handles POST /widgets: it decodes the request body, creates
// a new widget via the store, and writes it back as JSON.
func (h *Handler) CreateWidget(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}

	var req createWidgetRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if req.Name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}

	widget, err := h.store.CreateWidget(req.Name, req.Color)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "failed to create widget")
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(widget)
}

// errorResponse is the JSON shape returned for any non-2xx response.
type errorResponse struct {
	Error string `json:"error"`
}

// writeError writes a JSON error response with the given status code.
func writeError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(errorResponse{Error: message})
}
