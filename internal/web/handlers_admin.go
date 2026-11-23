package web

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/hearthservices/watchpickr/internal/store"
)

func (s *Server) handleAdmin(w http.ResponseWriter, r *http.Request) {
	data, err := s.adminData(r)
	if err != nil {
		s.fail(w, r, "could not load the admin page", err)
		return
	}
	s.render(w, r, http.StatusOK, "admin.html", data)
}

func (s *Server) adminData(r *http.Request) (map[string]any, error) {
	ctx := r.Context()
	poll := pollFrom(ctx)

	users, err := s.store.UserStats(ctx, poll.ID)
	if err != nil {
		return nil, err
	}
	allowSeries, err := s.store.BoolSetting(ctx, store.SettingAllowSeries, true)
	if err != nil {
		return nil, err
	}

	return map[string]any{
		"Title":       "Admin",
		"Users":       users,
		"AllowSeries": allowSeries,
		"MaxVotes":    poll.MaxVotes,
		"Policy":      store.PolicyRefuse,
	}, nil
}

func (s *Server) handleAdminSettings(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	poll := pollFrom(ctx)

	allow := r.FormValue("allow_series") != ""
	policy := r.FormValue("policy")
	rawMax := strings.TrimSpace(r.FormValue("max_votes"))

	// reject re-renders the form with what they typed, nothing is saved.
	reject := func(status int, msg string) {
		data, err := s.adminData(r)
		if err != nil {
			s.fail(w, r, "could not load the admin page", err)
			return
		}
		data["Error"] = msg
		data["AllowSeries"] = allow
		data["Policy"] = policy
		if n, err := strconv.Atoi(rawMax); err == nil {
			data["MaxVotes"] = n
		}
		s.render(w, r, status, "admin.html", data)
	}

	max, err := strconv.Atoi(rawMax)
	if err != nil || max < 1 {
		reject(http.StatusBadRequest, "The vote budget has to be a whole number, and greater than 0.")
		return
	}

	change, err := s.store.SetMaxVotes(ctx, poll.ID, max, policy)
	if errors.Is(err, store.ErrOverBudget) {
		reject(http.StatusConflict, fmt.Sprintf(
			"%s would be left over a limit of %d. Pick one of the other options, "+
				"or ask them to release some votes first.",
			strings.Join(change.OverBudget, " and "), max,
		))
		return
	}
	if err != nil {
		s.log.Error("set max votes", "err", err)
		s.adminFlash(w, r, "oops")
		return
	}

	// Only saved once the budget change went through
	if err := s.store.SetSetting(ctx, store.SettingAllowSeries, strconv.FormatBool(allow)); err != nil {
		s.fail(w, r, "could not save the settings", err)
		return
	}

	if change.Trimmed > 0 {
		s.adminFlash(w, r, "trimmed")
		return
	}
	s.adminFlash(w, r, "settingssaved")
}

func (s *Server) handleSetAdmin(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		s.adminFlash(w, r, "nouser")
		return
	}
	grant := r.FormValue("admin") == "1"

	switch err := s.store.SetAdmin(r.Context(), id, grant); {
	case err == nil:
		s.adminFlash(w, r, "adminchanged")
	case errors.Is(err, store.ErrLastAdmin):
		s.adminFlash(w, r, "lastadmin")
	case errors.Is(err, store.ErrNotFound):
		s.adminFlash(w, r, "nouser")
	default:
		s.log.Error("set admin", "user", id, "err", err)
		s.adminFlash(w, r, "oops")
	}
}

func (s *Server) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		s.adminFlash(w, r, "nouser")
		return
	}
	if id == userFrom(r.Context()).ID {
		s.adminFlash(w, r, "notyourself")
		return
	}

	switch err := s.store.DeleteUser(r.Context(), id); {
	case err == nil:
		s.adminFlash(w, r, "userdeleted")
	case errors.Is(err, store.ErrLastAdmin):
		s.adminFlash(w, r, "lastadmin")
	case errors.Is(err, store.ErrNotFound):
		s.adminFlash(w, r, "nouser")
	default:
		s.log.Error("delete user", "user", id, "err", err)
		s.adminFlash(w, r, "oops")
	}
}

func (s *Server) adminFlash(w http.ResponseWriter, r *http.Request, code string) {
	s.setFlash(w, code)
	http.Redirect(w, r, "/admin", http.StatusSeeOther)
}
