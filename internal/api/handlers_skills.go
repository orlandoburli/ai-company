package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/orlandoburli/ai-company/internal/skill"
	"github.com/orlandoburli/ai-company/internal/tenancy"
)

// registerSkillRoutes installs the tenant-scoped skill registry API.
func (s *Server) registerSkillRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/skills", s.authenticated(s.handleListSkills))
	mux.Handle("POST /api/skills", s.authenticated(s.handleCreateSkill))
	mux.Handle("GET /api/skills/{id}", s.authenticated(s.handleGetSkill))
	mux.Handle("PATCH /api/skills/{id}", s.authenticated(s.handlePatchSkill))
	mux.Handle("DELETE /api/skills/{id}", s.authenticated(s.handleDeleteSkill))
	mux.Handle("POST /api/skills/{id}/versions", s.authenticated(s.handleAddSkillVersion))
	mux.Handle("POST /api/skills/{id}/versions/{v}/approve", s.authenticated(s.handleApproveSkillVersion))
	mux.Handle("POST /api/skills/{id}/enable", s.authenticated(s.handleEnableSkill))
}

type skillCreateRequest struct {
	ProductID   *string `json:"product_id"`
	Name        string  `json:"name"`
	Slug        string  `json:"slug"`
	Description string  `json:"description"`
}

type skillPatchRequest struct {
	Name        *string `json:"name"`
	Slug        *string `json:"slug"`
	Description *string `json:"description"`
	Enabled     *bool   `json:"enabled"`
}

type skillVersionRequest struct {
	ContentMD  string  `json:"content_md"`
	AuthorKind string  `json:"author_kind"`
	AuthorID   *string `json:"author_id"`
}

type skillEnableRequest struct {
	Enabled *bool `json:"enabled"`
}

func (s *Server) skillStore() *skill.Store { return skill.NewStore(s.db) }

func (s *Server) handleListSkills(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.scope(w, r)
	if !ok {
		return
	}
	items, err := s.skillStore().List(r.Context(), sc, r.URL.Query().Get("product_id"))
	if err != nil {
		s.writeSkillError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"skills": items})
}

func (s *Server) handleCreateSkill(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.skillWriteScope(w, r)
	if !ok {
		return
	}
	var req skillCreateRequest
	if !decodeSkillJSON(w, r, &req) {
		return
	}
	item, err := s.skillStore().Create(r.Context(), sc, skill.NewSkill{
		ProductID: req.ProductID, Name: req.Name, Slug: req.Slug, Description: req.Description,
	})
	if err != nil {
		s.writeSkillError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"skill": item})
}

func (s *Server) handleGetSkill(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.scope(w, r)
	if !ok {
		return
	}
	store := s.skillStore()
	item, err := store.Get(r.Context(), sc, r.PathValue("id"))
	if err != nil {
		s.writeSkillError(w, err)
		return
	}
	versions, err := store.Versions(r.Context(), sc, item.ID)
	if err != nil {
		s.writeSkillError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"skill": item, "versions": versions})
}

func (s *Server) handlePatchSkill(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.skillWriteScope(w, r)
	if !ok {
		return
	}
	var req skillPatchRequest
	if !decodeSkillJSON(w, r, &req) {
		return
	}
	store := s.skillStore()
	var item skill.Skill
	var err error
	if req.Name != nil || req.Slug != nil || req.Description != nil {
		item, err = store.Update(r.Context(), sc, r.PathValue("id"), skill.Patch{
			Name: req.Name, Slug: req.Slug, Description: req.Description,
		})
	} else {
		item, err = store.Get(r.Context(), sc, r.PathValue("id"))
	}
	if err == nil && req.Enabled != nil {
		item, err = store.SetEnabled(r.Context(), sc, item.ID, *req.Enabled)
	}
	if err != nil {
		s.writeSkillError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"skill": item})
}

func (s *Server) handleDeleteSkill(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.skillWriteScope(w, r)
	if !ok {
		return
	}
	if err := s.skillStore().Delete(r.Context(), sc, r.PathValue("id")); err != nil {
		s.writeSkillError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleAddSkillVersion(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.skillWriteScope(w, r)
	if !ok {
		return
	}
	var req skillVersionRequest
	if !decodeSkillJSON(w, r, &req) {
		return
	}
	version, err := s.skillStore().AddVersion(r.Context(), sc, r.PathValue("id"), skill.NewVersion{
		ContentMD: req.ContentMD, AuthorKind: req.AuthorKind, AuthorID: req.AuthorID,
	})
	if err != nil {
		s.writeSkillError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"version": version})
}

func (s *Server) handleApproveSkillVersion(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.skillWriteScope(w, r)
	if !ok {
		return
	}
	n, err := strconv.Atoi(r.PathValue("v"))
	if err != nil || n < 1 {
		writeError(w, http.StatusBadRequest, "invalid_request", "Version must be a positive integer.")
		return
	}
	store := s.skillStore()
	version, found, err := store.Version(r.Context(), sc, r.PathValue("id"), n)
	if err != nil {
		s.writeSkillError(w, err)
		return
	}
	if !found {
		s.writeSkillError(w, skill.ErrNotFound)
		return
	}
	version, err = store.Approve(r.Context(), sc, version.ID, sc.UserID)
	if err != nil {
		s.writeSkillError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"version": version})
}

func (s *Server) handleEnableSkill(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.skillWriteScope(w, r)
	if !ok {
		return
	}
	var req skillEnableRequest
	if !decodeSkillJSON(w, r, &req) {
		return
	}
	if req.Enabled == nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "enabled is required.")
		return
	}
	item, err := s.skillStore().SetEnabled(r.Context(), sc, r.PathValue("id"), *req.Enabled)
	if err != nil {
		s.writeSkillError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"skill": item})
}

func (s *Server) skillWriteScope(w http.ResponseWriter, r *http.Request) (tenancy.Scope, bool) {
	sc, ok := s.scope(w, r)
	if !ok {
		return tenancy.Scope{}, false
	}
	if !sc.Role.CanManageSettings() {
		writeError(w, http.StatusForbidden, "forbidden", "Your role does not allow this action.")
		return tenancy.Scope{}, false
	}
	return sc, true
}

func decodeSkillJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "The request body is invalid.")
		return false
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		writeError(w, http.StatusBadRequest, "invalid_request", "The request body must contain one JSON object.")
		return false
	}
	return true
}

func (s *Server) writeSkillError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, skill.ErrNotFound):
		writeError(w, http.StatusNotFound, "skill_not_found", "That skill does not exist in this tenant.")
	case errors.Is(err, skill.ErrConflict):
		writeError(w, http.StatusConflict, "skill_slug_taken", "A skill with that slug already exists in this scope.")
	case errors.Is(err, skill.ErrInvalid):
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
	case errors.Is(err, tenancy.ErrForbidden):
		writeError(w, http.StatusForbidden, "forbidden", "Your role does not allow this action.")
	default:
		s.log.Error("skill handler", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "Something went wrong.")
	}
}
