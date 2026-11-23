package web

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/hearthservices/watchpickr/internal/store"
)

func (s *Server) handlePodium(w http.ResponseWriter, r *http.Request) {
	data, err := s.podiumData(r)
	switch {
	case err == nil:
		s.render(w, r, http.StatusOK, "podium.html", data)
	case errors.Is(err, store.ErrNotFound):
		s.handleNotFound(w, r)
	default:
		s.fail(w, r, "could not load the podium", err)
	}
}

// handlePodiumTop renders just the podium, for the live refresh.
func (s *Server) handlePodiumTop(w http.ResponseWriter, r *http.Request) {
	data, err := s.podiumData(r)
	if err != nil {
		s.fail(w, r, "could not load the podium", err)
		return
	}
	s.renderBlock(w, r, "podium.html", "podiumboard", data)
}

func (s *Server) handlePodiumEvents(w http.ResponseWriter, r *http.Request) {
	poll, err := s.podiumPoll(r.Context(), podiumCode(r))
	if err != nil {
		http.Error(w, "no such board", http.StatusNotFound)
		return
	}
	s.streamEvents(w, r, poll.ID)
}

// podiumPoll returns the session for a code, or the standing poll without one.
// PollByJoinCode only matches sessions, so a code can't reach the standing poll.
func (s *Server) podiumPoll(ctx context.Context, code string) (*store.Poll, error) {
	if code = strings.TrimSpace(code); code != "" {
		return s.store.PollByJoinCode(ctx, code)
	}
	return s.store.StandingPoll(ctx)
}

// podiumCode reads the code from the path, or from ?code= on the refresh
// endpoints.
func podiumCode(r *http.Request) string {
	if code := r.PathValue("code"); code != "" {
		return code
	}
	return r.URL.Query().Get("code")
}

func (s *Server) podiumData(r *http.Request) (map[string]any, error) {
	ctx := r.Context()

	poll, err := s.podiumPoll(ctx, podiumCode(r))
	if err != nil {
		return nil, err
	}

	var viewerID int64
	viewer := userFrom(ctx)
	canVote := false
	if viewer != nil {
		viewerID = viewer.ID
		// Buttons only when it's the board a logged in user belongs to.
		if own := pollFrom(ctx); own != nil && own.ID == poll.ID && !poll.Closed() {
			canVote = true
		}
	}

	movies, err := s.store.Movies(ctx, poll.ID, viewerID)
	if err != nil {
		return nil, err
	}
	var top []store.Movie
	for _, m := range movies {
		if !m.Watched() && m.Rank <= topCount && m.Votes > 0 {
			top = append(top, m)
		}
	}

	// How an admin can mark something watched after a session
	canMark := viewer != nil && viewer.IsAdmin && poll.IsSession()

	data := map[string]any{
		"Title":          poll.Name,
		"Poll":           poll,
		"Top":            top,
		"Code":           poll.JoinCode,
		"CanVote":        canVote,
		"CanMarkWatched": canMark,
		"Bare":           viewer == nil,
	}

	if canVote {
		used, err := s.store.VotesUsed(ctx, poll.ID, viewerID)
		if err != nil {
			return nil, err
		}
		data["VotesLeft"] = max(0, poll.MaxVotes-used)
	}
	return data, nil
}
