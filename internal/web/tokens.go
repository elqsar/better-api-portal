package web

import (
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/elqsar/better-api-portal/internal/store"
)

// patPrefix starts every personal access token, so it can be told from a
// CI token (ptk_) and spotted by secret scanners.
const patPrefix = "pat_"

// Personal access token limits.
const (
	maxTokensPerUser = 20
	maxTokenLabel    = 100
)

// tokenLifetimes are the expiries a user can pick, in days; the first is
// the default.
var tokenLifetimes = []int{90, 30, 365}

type tokensData struct {
	Tokens    []store.UserToken
	Lifetimes []int
	// New is the token just created, shown this once.
	New      string
	NewLabel string
	Error    string
	Label    string // the label to refill the form with after an error
	BaseURL  string
}

// tokens lists the user's personal access tokens. Managing tokens needs a
// session: a token can't mint or list tokens.
func (s *Server) tokens(w http.ResponseWriter, r *http.Request, u *User) {
	s.renderTokens(w, r, u, http.StatusOK, tokensData{})
}

func (s *Server) renderTokens(w http.ResponseWriter, r *http.Request, u *User, status int, d tokensData) {
	if u.ViaToken {
		s.error(w, r, u, http.StatusForbidden, "Sign in to manage tokens", "Tokens can't be managed with a token.")
		return
	}
	var err error
	if d.Tokens, err = s.Store.UserTokens(r.Context(), u.Subject); err != nil {
		s.fail(w, r, u, err)
		return
	}
	d.Lifetimes, d.BaseURL = tokenLifetimes, s.urls().Base
	s.render(w, r, status, "tokens", page{Title: "Personal access tokens", Nav: "tokens", User: u, Data: d})
}

func (s *Server) createToken(w http.ResponseWriter, r *http.Request, u *User) {
	if u.ViaToken {
		s.error(w, r, u, http.StatusForbidden, "Sign in to manage tokens", "Tokens can't be managed with a token.")
		return
	}
	label := strings.TrimSpace(r.PostFormValue("label"))
	days, _ := strconv.Atoi(r.PostFormValue("days"))
	switch {
	case label == "":
		s.renderTokens(w, r, u, http.StatusBadRequest, tokensData{Error: "Give the token a name, such as the tool that will use it."})
		return
	case utf8.RuneCountInString(label) > maxTokenLabel:
		s.renderTokens(w, r, u, http.StatusBadRequest, tokensData{Error: "The name is too long.", Label: label})
		return
	case !slices.Contains(tokenLifetimes, days):
		s.renderTokens(w, r, u, http.StatusBadRequest, tokensData{Error: "Pick one of the expiries offered.", Label: label})
		return
	}
	live, err := s.Store.UserTokens(r.Context(), u.Subject)
	if err != nil {
		s.fail(w, r, u, err)
		return
	}
	n := 0
	for _, t := range live {
		if t.ExpiresAt.After(time.Now()) {
			n++
		}
	}
	if n >= maxTokensPerUser {
		s.renderTokens(w, r, u, http.StatusBadRequest, tokensData{
			Error: "You have " + strconv.Itoa(n) + " tokens, the most you can have. Revoke one first.", Label: label})
		return
	}
	tok := patPrefix + randomString()
	if _, err := s.Store.CreateUserToken(r.Context(), hashID(tok), u.Session, label,
		time.Now().Add(time.Duration(days)*24*time.Hour)); err != nil {
		s.fail(w, r, u, err)
		return
	}
	// The token is shown in this response only, never again.
	w.Header().Set("Cache-Control", "no-store")
	s.renderTokens(w, r, u, http.StatusOK, tokensData{New: tok, NewLabel: label})
}

func (s *Server) revokeToken(w http.ResponseWriter, r *http.Request, u *User) {
	if u.ViaToken {
		s.error(w, r, u, http.StatusForbidden, "Sign in to manage tokens", "Tokens can't be managed with a token.")
		return
	}
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		s.error(w, r, u, http.StatusNotFound, "No such token", "You have no token with that id.")
		return
	}
	found, err := s.Store.RevokeUserToken(r.Context(), id, u.Session)
	if err != nil {
		s.fail(w, r, u, err)
		return
	}
	if !found {
		s.error(w, r, u, http.StatusNotFound, "No such token", "You have no token with that id.")
		return
	}
	http.Redirect(w, r, "/tokens", http.StatusSeeOther)
}
