package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/orlandoburli/ai-company/internal/usage"
)

// registerUsageRoutes exposes tenant-scoped usage reads to every role.
func (s *Server) registerUsageRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/usage", s.authenticated(s.handleUsage))
}

func (s *Server) handleUsage(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.scope(w, r)
	if !ok {
		return
	}
	query := r.URL.Query()
	var opts usage.Options
	for key, dst := range map[string]*time.Time{"since": &opts.Since, "until": &opts.Until} {
		value := strings.TrimSpace(query.Get(key))
		if value == "" {
			continue
		}
		parsed, err := time.Parse(time.RFC3339, value)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", key+" must be an RFC3339 timestamp.")
			return
		}
		*dst = parsed
	}
	opts.ProductID = strings.TrimSpace(query.Get("product_id"))

	buckets, err := usage.Aggregate(r.Context(), s.db, sc, opts)
	if err != nil {
		s.log.Error("usage aggregation", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "Something went wrong.")
		return
	}
	jobs := []usage.JobUsage{}
	if itemID := strings.TrimSpace(query.Get("item_id")); itemID != "" {
		jobs, err = usage.PerJob(r.Context(), s.db, sc, itemID, 100)
		if err != nil {
			s.log.Error("per-job usage", "err", err)
			writeError(w, http.StatusInternalServerError, "internal", "Something went wrong.")
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"usage": buckets, "jobs": jobs})
}
