package api

import (
	"database/sql"
	"errors"
	"net/http"

	"github.com/orlandoburli/ai-company/internal/push"
	"github.com/orlandoburli/ai-company/internal/tenancy"
)

// registerPushRoutes exposes browser subscription management. Only each user's
// own subscription keys are returned by this API.
func (s *Server) registerPushRoutes(mux *http.ServeMux) {
	mux.Handle("GET /api/push/subscriptions", s.authenticated(s.handleListPushSubscriptions))
	mux.Handle("POST /api/push/subscriptions", s.authenticated(s.requireRole(tenancy.Role.CanWriteWork, s.handleSubscribePush)))
	mux.Handle("DELETE /api/push/subscriptions/{id}", s.authenticated(s.requireRole(tenancy.Role.CanWriteWork, s.handleUnsubscribePush)))
}

type pushSubscriptionRequest struct {
	Endpoint       string `json:"endpoint"`
	P256DH         string `json:"p256dh"`
	Auth           string `json:"auth"`
	ExpirationTime *int64 `json:"expirationTime"`
	Keys           *struct {
		P256DH string `json:"p256dh"`
		Auth   string `json:"auth"`
	} `json:"keys"`
}

func (s *Server) handleListPushSubscriptions(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.scope(w, r)
	if !ok {
		return
	}
	rows, err := push.NewStore(s.db).List(r.Context(), sc, sc.UserID)
	if err != nil {
		s.writePushError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"subscriptions": rows})
}

func (s *Server) handleSubscribePush(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.scope(w, r)
	if !ok {
		return
	}
	var req pushSubscriptionRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Keys != nil {
		req.P256DH, req.Auth = req.Keys.P256DH, req.Keys.Auth
	}
	row, err := push.NewStore(s.db).Subscribe(r.Context(), sc, push.Subscription{UserID: sc.UserID, Endpoint: req.Endpoint, P256DH: req.P256DH, Auth: req.Auth})
	if err != nil {
		s.writePushError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"subscription": row})
}

func (s *Server) handleUnsubscribePush(w http.ResponseWriter, r *http.Request) {
	sc, ok := s.scope(w, r)
	if !ok {
		return
	}
	if err := push.NewStore(s.db).Unsubscribe(r.Context(), sc, r.PathValue("id")); err != nil {
		s.writePushError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) writePushError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, push.ErrNotFound), errors.Is(err, sql.ErrNoRows):
		writeError(w, http.StatusNotFound, "subscription_not_found", "That subscription does not exist for this user.")
	case errors.Is(err, push.ErrInvalid):
		writeError(w, http.StatusUnprocessableEntity, "invalid_subscription", "The browser subscription is invalid.")
	case errors.Is(err, tenancy.ErrForbidden):
		writeError(w, http.StatusForbidden, "forbidden", "Your role does not allow this action.")
	default:
		s.log.Error("push subscription handler", "err", err)
		writeError(w, http.StatusInternalServerError, "internal", "Something went wrong.")
	}
}
