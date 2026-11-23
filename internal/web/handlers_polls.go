package web

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/hearthservices/watchpickr/internal/auth"
	"github.com/hearthservices/watchpickr/internal/store"
)

// guestSessionTTL is short, a guest account is only for one evening.
const guestSessionTTL = 12 * time.Hour

/* ----------------------------------------------------------- joining --- */

func (s *Server) handleJoinForm(w http.ResponseWriter, r *http.Request) {
	poll, ok := s.joinablePoll(w, r)
	if !ok {
		return
	}
	token, err := s.anonCSRF(w, r)
	if err != nil {
		s.fail(w, r, "could not prepare the form", err)
		return
	}
	s.render(w, r, http.StatusOK, "join.html", map[string]any{
		"Title": "Join " + poll.Name,
		"Poll":  poll,
		"Code":  r.PathValue("code"),
		"CSRF":  token,
	})
}

func (s *Server) handleJoin(w http.ResponseWriter, r *http.Request) {
	poll, ok := s.joinablePoll(w, r)
	if !ok {
		return
	}
	name := strings.TrimSpace(r.FormValue("name"))

	reject := func(status int, msg string) {
		token, _ := s.anonCSRF(w, r)
		s.render(w, r, status, "join.html", map[string]any{
			"Title": "Join " + poll.Name,
			"Poll":  poll,
			"Code":  r.PathValue("code"),
			"Error": msg,
			"Name":  name,
			"CSRF":  token,
		})
	}

	if !s.checkAnonCSRF(r) {
		reject(http.StatusForbidden, "That form expired. Please try again.")
		return
	}
	clean, err := auth.ValidateUsername(name)
	if err != nil {
		reject(http.StatusBadRequest, err.Error())
		return
	}

	guest, err := s.store.CreateGuest(r.Context(), poll.ID, clean)
	if err != nil {
		if errors.Is(err, store.ErrDuplicateUser) {
			reject(http.StatusConflict, "Somebody here is already using that name. Please pick another.")
			return
		}
		s.fail(w, r, "could not add you to the board", err)
		return
	}
	if err := s.startSessionFor(w, r, guest, guestSessionTTL); err != nil {
		s.fail(w, r, "could not sign you in", err)
		return
	}
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// joinablePoll looks up the code in the URL. If it's no good, it writes the
// response itself and returns false.
func (s *Server) joinablePoll(w http.ResponseWriter, r *http.Request) (*store.Poll, bool) {
	code := r.PathValue("code")

	// Join codes are short, so they get rate limited like passwords.
	if !s.logins.allow("join:" + clientIP(r, s.cfg.TrustedProxies)) {
		http.Error(w, "Too many attempts. Wait a few minutes.", http.StatusTooManyRequests)
		return nil, false
	}

	poll, err := s.store.PollByJoinCode(r.Context(), code)
	if err != nil {
		s.notFound(w, r, "No such board",
			"That link has expired, or the code was mistyped.")
		return nil, false
	}
	if poll.Closed() {
		s.notFound(w, r, poll.Name+" is closed",
			"Voting has finished for this one.")
		return nil, false
	}
	return poll, true
}

/* ------------------------------------------------- managing sessions --- */

func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	polls, err := s.store.SessionPolls(r.Context())
	if err != nil {
		s.fail(w, r, "could not list the sessions", err)
		return
	}
	s.render(w, r, http.StatusOK, "sessions.html", map[string]any{
		"Title":    "Session boards",
		"Sessions": polls,
		"BaseURL":  baseURL(r),
	})
}

func (s *Server) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		name = "Movie night"
	}
	votes := 3
	if n := parsePositive(r.FormValue("max_votes")); n > 0 {
		votes = n
	}

	code, err := auth.JoinCode()
	if err != nil {
		s.fail(w, r, "could not create the session", err)
		return
	}
	if _, err := s.store.CreateSessionPoll(r.Context(), name, votes, code); err != nil {
		s.fail(w, r, "could not create the session", err)
		return
	}
	s.setFlash(w, "sessionmade")
	http.Redirect(w, r, "/sessions", http.StatusSeeOther)
}

func (s *Server) handleCloseSession(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		s.setFlash(w, "nosession")
		http.Redirect(w, r, "/sessions", http.StatusSeeOther)
		return
	}
	switch err := s.store.ClosePoll(r.Context(), id); {
	case err == nil:
		s.setFlash(w, "sessionclosed")
	case errors.Is(err, store.ErrNotFound):
		// No rows: already closed, deleted, or not a session
		s.setFlash(w, "wasclosed")
	default:
		s.log.Error("close session", "poll", id, "err", err)
		s.setFlash(w, "oops")
	}
	http.Redirect(w, r, "/sessions", http.StatusSeeOther)
}

func (s *Server) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		s.setFlash(w, "nosession")
		http.Redirect(w, r, "/sessions", http.StatusSeeOther)
		return
	}
	switch err := s.store.DeletePoll(r.Context(), id); {
	case err == nil:
		s.setFlash(w, "sessiondeleted")
	case errors.Is(err, store.ErrNotFound):
		s.setFlash(w, "nosession")
	default:
		s.log.Error("delete session", "poll", id, "err", err)
		s.setFlash(w, "oops")
	}
	http.Redirect(w, r, "/sessions", http.StatusSeeOther)
}

// handleSessionWatched marks a film watched on a session board.
func (s *Server) handleSessionWatched(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	movieID, err := strconv.ParseInt(r.PathValue("movie"), 10, 64)
	if !ok || err != nil {
		s.flashBack(w, r, "gone", 0)
		return
	}
	poll, err := s.store.PollByID(r.Context(), id)
	if err != nil || !poll.IsSession() {
		s.flashBack(w, r, "nosession", 0)
		return
	}

	switch err := s.store.MarkWatched(r.Context(), poll.ID, movieID); {
	case err == nil:
		s.hub.broadcast(poll.ID, "watched", movieID)
		s.flashBack(w, r, "watched", 0)
	case errors.Is(err, store.ErrMovieWatched):
		s.flashBack(w, r, "iswatched", 0)
	default:
		s.log.Error("mark watched", "err", err, "poll", poll.ID, "movie", movieID)
		s.flashBack(w, r, "oops", 0)
	}
}

/* ------------------------------------------------- helpers --- */

func baseURL(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https" {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

func parsePositive(raw string) int {
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || n < 1 {
		return 0
	}
	return n
}
