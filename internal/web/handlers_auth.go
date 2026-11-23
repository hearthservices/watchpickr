package web

import (
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/hearthservices/watchpickr/internal/auth"
	"github.com/hearthservices/watchpickr/internal/store"
)

func (s *Server) handleLoginForm(w http.ResponseWriter, r *http.Request) {
	if user, _ := s.session(r); user != nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	token, err := s.anonCSRF(w, r)
	if err != nil {
		s.fail(w, r, "could not prepare the form", err)
		return
	}
	s.render(w, r, http.StatusOK, "login.html", map[string]any{
		"Title": "Log in",
		"CSRF":  token,
	})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	username := strings.TrimSpace(r.FormValue("username"))

	reject := func(status int, msg string) {
		token, _ := s.anonCSRF(w, r)
		s.render(w, r, status, "login.html", map[string]any{
			"Title": "Log in",
			"Error": msg,
			"Name":  username,
			"CSRF":  token,
		})
	}

	if !s.checkAnonCSRF(r) {
		reject(http.StatusForbidden, "That form expired. Please try again.")
		return
	}

	// Keyed on username and IP, so one person's typos can't lock out someone else.
	key := "login:" + strings.ToLower(username) + "|" + clientIP(r, s.cfg.TrustedProxies)
	if !s.logins.allow(key) {
		reject(http.StatusTooManyRequests, "Too many attempts. Wait a few minutes and try again.")
		return
	}

	user, err := s.store.Authenticate(r.Context(), username, r.FormValue("password"))
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) {
			s.log.Error("authenticate", "err", err)
		}
		reject(http.StatusUnauthorized, "Wrong username or password.")
		return
	}

	s.logins.clear(key)
	if err := s.startSession(w, r, user); err != nil {
		s.fail(w, r, "could not log you in", err)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) handleRegisterForm(w http.ResponseWriter, r *http.Request) {
	if user, _ := s.session(r); user != nil {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	token, err := s.anonCSRF(w, r)
	if err != nil {
		s.fail(w, r, "could not prepare the form", err)
		return
	}
	s.render(w, r, http.StatusOK, "register.html", map[string]any{
		"Title":       "Create an account",
		"NeedsCode":   s.cfg.RegistrationCode != "",
		"MinPassword": auth.MinPasswordLen,
		"CSRF":        token,
	})
}

func (s *Server) handleRegister(w http.ResponseWriter, r *http.Request) {
	username := strings.TrimSpace(r.FormValue("username"))

	reject := func(status int, msg string) {
		token, _ := s.anonCSRF(w, r)
		s.render(w, r, status, "register.html", map[string]any{
			"Title":       "Create an account",
			"Error":       msg,
			"Name":        username,
			"NeedsCode":   s.cfg.RegistrationCode != "",
			"MinPassword": auth.MinPasswordLen,
			"CSRF":        token,
		})
	}

	if !s.checkAnonCSRF(r) {
		reject(http.StatusForbidden, "That form expired, please try again.")
		return
	}
	if !s.logins.allow("register:" + clientIP(r, s.cfg.TrustedProxies)) {
		reject(http.StatusTooManyRequests, "Too many attempts. Wait a few minutes and try again.")
		return
	}
	if code := s.cfg.RegistrationCode; code != "" && strings.TrimSpace(r.FormValue("code")) != code {
		reject(http.StatusForbidden, "That invite code is not correct.")
		return
	}

	name, err := auth.ValidateUsername(username)
	if err != nil {
		reject(http.StatusBadRequest, err.Error())
		return
	}
	password := r.FormValue("password")
	if err := auth.ValidatePassword(password); err != nil {
		reject(http.StatusBadRequest, err.Error())
		return
	}

	hash, err := auth.HashPassword(password)
	if err != nil {
		s.fail(w, r, "could not create the account", err)
		return
	}
	user, err := s.store.CreateUser(r.Context(), name, hash, false)
	if err != nil {
		if errors.Is(err, store.ErrDuplicateUser) {
			reject(http.StatusConflict, "That username is taken")
			return
		}
		s.fail(w, r, "could not create the account", err)
		return
	}

	if err := s.startSession(w, r, user); err != nil {
		s.fail(w, r, "the account was created but signing in failed", err)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil && c.Value != "" {
		if err := s.store.DeleteSession(r.Context(), c.Value); err != nil {
			s.log.Error("delete session", "err", err)
		}
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		Secure:   s.cfg.SecureCookies,
		SameSite: http.SameSiteLaxMode,
	})
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

func (s *Server) startSession(w http.ResponseWriter, r *http.Request, user *store.User) error {
	return s.startSessionFor(w, r, user, s.cfg.SessionTTL)
}

// startSessionFor starts a session with the given lifetime. A cookie for a
// party guest shouldn't last a month.
func (s *Server) startSessionFor(w http.ResponseWriter, r *http.Request, user *store.User, ttl time.Duration) error {
	token, err := auth.Token()
	if err != nil {
		return err
	}
	csrf, err := auth.Token()
	if err != nil {
		return err
	}
	expires := time.Now().Add(ttl)
	if err := s.store.CreateSession(r.Context(), user.ID, token, csrf, expires); err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		Expires:  expires,
		HttpOnly: true,
		Secure:   s.cfg.SecureCookies,
		SameSite: http.SameSiteLaxMode,
	})
	return nil
}

// fail logs the real error and shows the reader a short explanation.
func (s *Server) fail(w http.ResponseWriter, r *http.Request, msg string, err error) {
	s.log.Error(msg, "err", err, "path", r.URL.Path)
	http.Error(w, "Something went wrong. Please try again shortly.", http.StatusInternalServerError)
}
