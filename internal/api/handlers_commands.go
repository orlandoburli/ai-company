package api

import (
	"database/sql"
	"errors"
	"net/http"

	"github.com/orlandoburli/ai-company/internal/command"
	"github.com/orlandoburli/ai-company/internal/tenancy"
)

func (s *Server) registerCommandRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/notification-channels", s.authenticated(s.handleListCommandChannels))
	mux.Handle("POST /api/notification-channels", s.authenticated(s.requireRole(tenancy.Role.CanManageSettings, s.handleCreateCommandChannel)))
	mux.Handle("PATCH /api/notification-channels/{id}", s.authenticated(s.requireRole(tenancy.Role.CanManageSettings, s.handlePatchCommandChannel)))
	mux.Handle("DELETE /api/notification-channels/{id}", s.authenticated(s.requireRole(tenancy.Role.CanManageSettings, s.handleDeleteCommandChannel)))
}

// command channel responses include the template as non-secret configuration;
// any credentials used by an invoked program belong in the OS keychain.
func (s *Server) handleListCommandChannels(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.scope(w, r)
	if !ok {
		return
	}
	items, err := command.List(r.Context(), s.db, sc)
	if err != nil {
		s.writeCommandError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"channels": items})
}

type commandChannelRequest struct {
	Name            string   `json:"name"`
	CommandTemplate string   `json:"command_template"`
	Events          []string `json:"events"`
	Enabled         *bool    `json:"enabled"`
}
type commandChannelPatchRequest struct {
	Name            *string   `json:"name"`
	CommandTemplate *string   `json:"command_template"`
	Events          *[]string `json:"events"`
	Enabled         *bool     `json:"enabled"`
}

func (s *Server) handleCreateCommandChannel(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.scope(w, r)
	if !ok {
		return
	}
	var req commandChannelRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	item, err := command.Create(r.Context(), s.db, sc, command.Channel{Name: req.Name, CommandTemplate: req.CommandTemplate, Events: req.Events, Enabled: enabled})
	if err != nil {
		s.writeCommandError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"channel": item})
}

func (s *Server) handlePatchCommandChannel(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.scope(w, r)
	if !ok {
		return
	}
	var req commandChannelPatchRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	item, err := command.Update(r.Context(), s.db, sc, r.PathValue("id"), command.Patch{Name: req.Name, CommandTemplate: req.CommandTemplate, Events: req.Events, Enabled: req.Enabled})
	if err != nil {
		s.writeCommandError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"channel": item})
}

func (s *Server) handleDeleteCommandChannel(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.scope(w, r)
	if !ok {
		return
	}
	if err := command.Delete(r.Context(), s.db, sc, r.PathValue("id")); err != nil {
		s.writeCommandError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) writeCommandError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, sql.ErrNoRows):
		writeError(w, http.StatusNotFound, "channel_not_found", "That notification channel does not exist in this tenant.")
	case errors.Is(err, tenancy.ErrForbidden):
		writeError(w, http.StatusForbidden, "forbidden", "Your role does not allow this action.")
	case errors.Is(err, command.ErrInvalid):
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
	default:
		s.log.Error("command channel handler", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "Something went wrong.")
	}
}
