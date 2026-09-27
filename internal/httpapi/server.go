// Package httpapi is the portal's REST API, /api/v1
// (docs/spec/05-architecture.md §Portal's own REST API).
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/elqsar/better-api-portal/internal/config"
	"github.com/elqsar/better-api-portal/internal/store"
)

// Identity is an authenticated caller.
type Identity struct {
	// Repo is the repo the caller acts for, as its CI subject (e.g. the
	// GitHub "repository" claim). API ids are claimed by it.
	Repo string
	// Actor names the caller in the audit log.
	Actor string
	// Ref, Commit and RunURL describe the CI run, when known.
	Ref, Commit, RunURL string
	// CanPush is false for a caller that may only read and check, such as
	// a CI job on a pull request's ref.
	CanPush bool
}

// ErrUnauthenticated is what an Authenticator returns, possibly wrapped, when
// the request carries no acceptable credentials.
var ErrUnauthenticated = errors.New("unauthenticated")

// Authenticator identifies the caller of a request.
type Authenticator interface {
	Authenticate(r *http.Request) (*Identity, error)
}

// Server serves the REST API.
type Server struct {
	Store  *store.Store
	Config *config.Config
	Auth   Authenticator
	Log    *slog.Logger
}

// Handler returns the API's routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	mux.HandleFunc("GET /readyz", s.ready)
	mux.Handle("POST /api/v1/push", s.authed(s.push(false)))
	mux.Handle("POST /api/v1/check", s.authed(s.push(true)))
	mux.Handle("GET /api/v1/apis/{id}/versions/{version}/bundle", s.authed(s.bundle))
	return mux
}

func (s *Server) ready(w http.ResponseWriter, r *http.Request) {
	if err := s.Store.Ping(r.Context()); err != nil {
		s.Log.Warn("not ready", "err", err)
		http.Error(w, "database unavailable", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusOK)
}

type identityKey struct{}

// authed authenticates the request and passes the identity on in its
// context.
func (s *Server) authed(h http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := s.Auth.Authenticate(r)
		if err != nil {
			s.Log.Info("authentication failed", "path", r.URL.Path, "err", err)
			if errors.Is(err, ErrUnauthenticated) {
				writeError(w, http.StatusUnauthorized, err.Error())
			} else {
				writeError(w, http.StatusInternalServerError, "authentication failed")
			}
			return
		}
		h(w, r.WithContext(context.WithValue(r.Context(), identityKey{}, id)))
	})
}

func identity(r *http.Request) *Identity { return r.Context().Value(identityKey{}).(*Identity) }

// url is the public link to a portal page.
func (s *Server) url(path string) string {
	if s.Config == nil {
		return path
	}
	return strings.TrimSuffix(s.Config.Server.PublicURL, "/") + path
}

// ErrorResponse is the body of every non-2xx JSON response.
type ErrorResponse struct {
	Error string `json:"error"`
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, ErrorResponse{msg})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.Encode(v)
}
