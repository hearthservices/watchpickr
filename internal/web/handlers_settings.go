package web

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/hearthservices/watchpickr/internal/auth"
	"github.com/hearthservices/watchpickr/internal/store"
)

func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	s.render(w, r, http.StatusOK, "settings.html", settingsData(r, ""))
}

// settingsData fills the form. An empty typed shows the current name, otherwise
// a rejected name stays in the field. Nobody likes retyping.
func settingsData(r *http.Request, typed string) map[string]any {
	if typed == "" {
		typed = userFrom(r.Context()).Username
	}
	return map[string]any{
		"Title":       "Settings",
		"Name":        typed,
		"MinPassword": auth.MinPasswordLen,
	}
}

func (s *Server) handlePassword(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())

	// Separate key, so the error shows next to the password form
	reject := func(status int, msg string) {
		data := settingsData(r, "")
		data["PasswordError"] = msg
		s.render(w, r, status, "settings.html", data)
	}

	if user.Kind != store.KindMember {
		reject(http.StatusForbidden, "Guest accounts do not have a password.")
		return
	}

	// Bcrypt is slow, so rate limit this or it's an easy way to load the server.
	key := "password:" + strconv.FormatInt(user.ID, 10) + "|" + clientIP(r, s.cfg.TrustedProxies)
	if !s.logins.allow(key) {
		reject(http.StatusTooManyRequests, "Too many attempts. Wait a few minutes and try again.")
		return
	}

	next := r.FormValue("new")
	if next != r.FormValue("repeat") {
		reject(http.StatusBadRequest, "The two new entered passwords do not match.")
		return
	}
	if err := auth.ValidatePassword(next); err != nil {
		reject(http.StatusBadRequest, err.Error())
		return
	}

	switch err := s.store.ChangePassword(r.Context(), user.ID, r.FormValue("current"), next); {
	case err == nil:
		s.logins.clear(key)
		if c, err := r.Cookie(sessionCookie); err == nil {
			if err := s.store.DeleteOtherSessions(r.Context(), user.ID, c.Value); err != nil {
				s.log.Error("clear other sessions", "user", user.ID, "err", err)
			}
		}
		s.settingsFlash(w, r, "passwordchanged")
	case errors.Is(err, store.ErrWrongPassword):
		reject(http.StatusUnauthorized, "That is not your current password.")
	case errors.Is(err, store.ErrNotFound):
		reject(http.StatusNotFound, "Account not found. You can try logging out and in again to clear a stale state.")
	default:
		s.log.Error("change password", "user", user.ID, "err", err)
		reject(http.StatusInternalServerError, "Something went wrong. Please try again.")
	}
}

func (s *Server) handleRename(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())
	typed := strings.TrimSpace(r.FormValue("username"))

	reject := func(status int, msg string) {
		data := settingsData(r, typed)
		data["Error"] = msg
		s.render(w, r, status, "settings.html", data)
	}

	name, err := auth.ValidateUsername(typed)
	if err != nil {
		reject(http.StatusBadRequest, err.Error())
		return
	}
	if name == user.Username {
		s.settingsFlash(w, r, "namesame")
		return
	}

	switch err := s.store.Rename(r.Context(), user.ID, name); {
	case err == nil:
		s.hub.broadcast(pollFrom(r.Context()).ID, "renamed", user.ID)
		s.settingsFlash(w, r, "namechanged")
	case errors.Is(err, store.ErrDuplicateUser):
		reject(http.StatusConflict, "Somebody is already using that name.")
	case errors.Is(err, store.ErrNotFound):
		reject(http.StatusNotFound, "Could not find your account. Try logging in again.")
	default:
		s.log.Error("rename user", "user", user.ID, "err", err)
		reject(http.StatusInternalServerError, "Something went wrong. Please try again.")
	}
}

func (s *Server) settingsFlash(w http.ResponseWriter, r *http.Request, code string) {
	s.setFlash(w, code)
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}
