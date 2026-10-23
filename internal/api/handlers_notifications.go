package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/orlandoburli/ai-company/internal/notification"
	"github.com/orlandoburli/ai-company/internal/tenancy"
)

type notificationResponse struct {
	ID        string            `json:"id"`
	TenantID  string            `json:"tenant_id"`
	UserID    string            `json:"user_id"`
	Kind      notification.Kind `json:"kind"`
	TitleMD   string            `json:"title_md"`
	BodyMD    string            `json:"body_md"`
	Link      string            `json:"link"`
	Payload   json.RawMessage   `json:"payload"`
	ReadAt    *time.Time        `json:"read_at"`
	CreatedAt time.Time         `json:"created_at"`
}

func notificationDTO(entry notification.Entry) notificationResponse {
	return notificationResponse{
		ID: entry.ID, TenantID: entry.TenantID, UserID: entry.UserID, Kind: entry.Kind,
		TitleMD: entry.TitleMD, BodyMD: entry.BodyMD, Link: entry.Link, Payload: entry.Payload,
		ReadAt: entry.ReadAt, CreatedAt: entry.CreatedAt,
	}
}

// registerNotificationRoutes installs the authenticated notification centre.
func (s *Server) registerNotificationRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/notifications", s.authenticated(s.handleListNotifications))
	mux.Handle("POST /api/notifications/{id}/read", s.authenticated(s.requireRole(tenancy.Role.CanWriteWork, s.handleMarkNotificationRead)))
}

func (s *Server) handleListNotifications(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.scope(w, r)
	if !ok {
		return
	}
	if s.notify == nil {
		writeWorkflowUnavailable(w)
		return
	}
	unreadOnly := false
	if value := r.URL.Query().Get("unread"); value != "" {
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "unread must be true or false.")
			return
		}
		unreadOnly = parsed
	}
	entries, err := s.notify.List(r.Context(), sc, sc.UserID, unreadOnly, queryInt(r, "limit", 50))
	if err != nil {
		s.writeNotificationError(w, err)
		return
	}
	unread, err := s.notify.CountUnread(r.Context(), sc, sc.UserID)
	if err != nil {
		s.writeNotificationError(w, err)
		return
	}
	response := make([]notificationResponse, 0, len(entries))
	for _, entry := range entries {
		response = append(response, notificationDTO(entry))
	}
	writeJSON(w, http.StatusOK, map[string]any{"notifications": response, "unread": unread})
}

func (s *Server) handleMarkNotificationRead(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.scope(w, r)
	if !ok {
		return
	}
	if s.notify == nil {
		writeWorkflowUnavailable(w)
		return
	}
	entry, err := s.notify.MarkRead(r.Context(), sc, r.PathValue("id"), sc.UserID)
	if err != nil {
		s.writeNotificationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"notification": notificationDTO(entry)})
}

func (s *Server) writeNotificationError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, notification.ErrNotFound):
		writeError(w, http.StatusNotFound, "notification_not_found", "That notification does not exist for this user.")
	case errors.Is(err, notification.ErrAlreadyRead):
		writeError(w, http.StatusConflict, "notification_already_read", "That notification has already been read.")
	default:
		s.log.Error("notification handler", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "Something went wrong.")
	}
}

func writeWorkflowUnavailable(w http.ResponseWriter) {
	writeError(w, http.StatusServiceUnavailable, "workflow_unavailable", "This workflow service is unavailable.")
}
