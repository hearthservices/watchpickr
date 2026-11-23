package web

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/hearthservices/watchpickr/internal/imdb"
	"github.com/hearthservices/watchpickr/internal/metadata"
	"github.com/hearthservices/watchpickr/internal/store"
)

const maxTitleLen = 200

func (s *Server) handleAddMovie(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())
	poll := pollFrom(r.Context())

	if poll.Closed() {
		s.flashBack(w, r, "closed", 0)
		return
	}

	imdbID, ok := imdb.ParseID(r.FormValue("link"))
	if !ok {
		s.flashBack(w, r, "badlink", 0)
		return
	}
	title := strings.TrimSpace(r.FormValue("title"))
	typedTitle := title != ""
	if !typedTitle {
		if !s.meta.Enabled() {
			s.flashBack(w, r, "notitle", 0)
			return
		}
		title = imdbID // Placeholder until the lookup fills it in
	}
	if utf8.RuneCountInString(title) > maxTitleLen {
		title = string([]rune(title)[:maxTitleLen])
	}

	allowSeries, err := s.store.BoolSetting(r.Context(), store.SettingAllowSeries, true)
	if err != nil {
		s.log.Error("read allow_series", "err", err)
		allowSeries = true
	}
	if !allowSeries {
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		kind, err := s.meta.Kind(ctx, imdbID)
		cancel()

		switch {
		case err != nil:
			// TMDb is down, let it through. The series switch is just a house rule.
			s.log.Warn("could not check kind, allowing", "imdb", imdbID, "err", err)
		case kind == metadata.KindSeries:
			s.flashBack(w, r, "noseries", 0)
			return
		}
	}

	// A guest's suggestion stays in that session
	var id int64
	if poll.IsSession() {
		id, err = s.store.AddSessionMovie(r.Context(), poll.ID, imdbID, title, user.ID)
	} else {
		id, err = s.store.AddMovie(r.Context(), imdbID, title, user.ID)
	}

	switch {
	case err == nil:
		s.enrich(poll.ID, id, imdbID, !typedTitle)
		s.hub.broadcast(poll.ID, "added", id)
		s.flashBack(w, r, "added", id)
	case errors.Is(err, store.ErrDuplicateMovie):
		s.flashBack(w, r, "dupe", 0)
	default:
		s.log.Error("add movie", "err", err, "imdb", imdbID)
		s.flashBack(w, r, "oops", 0)
	}
}

func (s *Server) handleVote(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())
	id, ok := pathID(r)
	if !ok {
		s.flashBack(w, r, "gone", 0)
		return
	}
	poll := pollFrom(r.Context())

	switch err := s.store.Vote(r.Context(), poll.ID, user.ID, id, poll.MaxVotes); {
	case err == nil:
		s.hub.broadcast(poll.ID, "voted", id)
		s.flashBack(w, r, "voted", id)
	case errors.Is(err, store.ErrPollClosed):
		s.flashBack(w, r, "closed", id)
	case errors.Is(err, store.ErrNoVotesLeft):
		s.flashBack(w, r, "novotes", id)
	case errors.Is(err, store.ErrAlreadyVoted):
		s.flashBack(w, r, "already", id)
	case errors.Is(err, store.ErrMovieWatched):
		s.flashBack(w, r, "iswatched", id)
	case errors.Is(err, store.ErrNotFound):
		s.flashBack(w, r, "gone", 0)
	default:
		s.log.Error("vote", "err", err, "movie", id)
		s.flashBack(w, r, "oops", id)
	}
}

func (s *Server) handleUnvote(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())
	id, ok := pathID(r)
	if !ok {
		s.flashBack(w, r, "gone", 0)
		return
	}
	poll := pollFrom(r.Context())

	// A closed board's result is final, so no taking votes back either.
	if poll.Closed() {
		s.flashBack(w, r, "closed", id)
		return
	}

	switch err := s.store.Unvote(r.Context(), poll.ID, user.ID, id); {
	case err == nil:
		s.hub.broadcast(poll.ID, "unvoted", id)
		s.flashBack(w, r, "unvoted", id)
	case errors.Is(err, store.ErrNotFound):
		s.flashBack(w, r, "gone", id)
	default:
		s.log.Error("unvote", "err", err, "movie", id)
		s.flashBack(w, r, "oops", id)
	}
}

func (s *Server) handleMarkWatched(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		s.flashBack(w, r, "gone", 0)
		return
	}
	poll := pollFrom(r.Context())

	switch err := s.store.MarkWatched(r.Context(), poll.ID, id); {
	case err == nil:
		s.hub.broadcast(poll.ID, "watched", id)
		s.flashBack(w, r, "watched", id)
	case errors.Is(err, store.ErrMovieWatched):
		s.flashBack(w, r, "iswatched", id)
	default:
		s.log.Error("mark watched", "err", err, "movie", id)
		s.flashBack(w, r, "oops", id)
	}
}

func (s *Server) handleReopen(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r)
	if !ok {
		s.flashBack(w, r, "gone", 0)
		return
	}
	poll := pollFrom(r.Context())

	switch err := s.store.Reopen(r.Context(), poll.ID, id); {
	case err == nil:
		s.hub.broadcast(poll.ID, "reopened", id)
		s.flashBack(w, r, "reopened", id)
	case errors.Is(err, store.ErrNotFound):
		s.flashBack(w, r, "gone", id)
	default:
		s.log.Error("reopen", "err", err, "movie", id)
		s.flashBack(w, r, "oops", id)
	}
}

// handleDeleteMovie lets admins remove any film, and others their own
// suggestion while it has no votes and isn't watched. The template hides the
// button, but the rules are checked here.
func (s *Server) handleDeleteMovie(w http.ResponseWriter, r *http.Request) {
	user := userFrom(r.Context())
	id, ok := pathID(r)
	if !ok {
		s.flashBack(w, r, "gone", 0)
		return
	}
	poll := pollFrom(r.Context())

	movie, err := s.store.MovieByID(r.Context(), poll.ID, user.ID, id)
	if errors.Is(err, store.ErrNotFound) {
		s.flashBack(w, r, "gone", 0)
		return
	} else if err != nil {
		s.log.Error("load movie", "err", err, "movie", id)
		s.flashBack(w, r, "oops", 0)
		return
	}

	if !user.IsAdmin {
		if movie.AddedByID != user.ID {
			s.flashBack(w, r, "notyours", id)
			return
		}
		if movie.Votes > 0 {
			s.flashBack(w, r, "hasvotes", id)
			return
		}
		if movie.Watched() {
			s.flashBack(w, r, "notyours", id)
			return
		}
	}

	switch err := s.store.DeleteMovie(r.Context(), id); {
	case err == nil:
		s.hub.broadcast(poll.ID, "removed", id)
		s.flashBack(w, r, "removed", 0)
	case errors.Is(err, store.ErrNotFound):
		// Someone else removed it first, same result
		s.flashBack(w, r, "gone", 0)
	default:
		s.log.Error("delete movie", "err", err, "movie", id)
		s.flashBack(w, r, "oops", id)
	}
}

/* -------------------------------------------------------------- helpers --- */

func pathID(r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id, err == nil && id > 0
}

const lookupTimeout = 20 * time.Second

// enrich fetches metadata in the background. It uses the server's context since
// the request's is cancelled once the response is sent.
func (s *Server) enrich(pollID, movieID int64, imdbID string, fillTitle bool) {
	if !s.meta.Enabled() {
		return
	}
	s.bgWork.Add(1)
	go func() {
		defer s.bgWork.Done()

		ctx, cancel := context.WithTimeout(s.bg, lookupTimeout)
		defer cancel()

		res, err := s.meta.Lookup(ctx, imdbID)
		if err != nil {
			s.log.Warn("metadata lookup incomplete", "imdb", imdbID, "partial", res != nil, "err", err)
			return
		}
		if res == nil {
			return
		}

		md := store.Metadata{
			Year:       res.Year,
			RuntimeMin: res.RuntimeMin,
			Synopsis:   res.Synopsis,
			Kind:       res.Kind,
			Genres:     res.Genres,
		}
		if fillTitle {
			md.Title = res.Title
		}
		for _, r := range res.Ratings {
			md.Ratings = append(md.Ratings, store.Rating{
				Source:    r.Source,
				Score:     r.Score,
				VoteCount: r.VoteCount,
			})
		}
		if file, err := s.posters.Save(ctx, imdbID, res.PosterURL); err != nil {
			s.log.Warn("poster download failed", "imdb", imdbID, "err", err)
		} else {
			md.PosterFile = file
		}

		if err := s.store.SaveMetadata(ctx, movieID, md); err != nil {
			s.log.Error("save metadata", "imdb", imdbID, "err", err)
		}
		s.hub.broadcast(pollID, "enriched", movieID)
	}()
}

// boardParams are the query keys kept when returning to the board.
var boardParams = []string{"q", "genre", "kind", "min_rating", "sort", "show", "view"}

// returnTarget rebuilds the board URL from allowed keys only, so it can't be
// used as an open redirect.
func returnTarget(r *http.Request) string {
	// Votes cast from the podium go back there. The code is escaped, so a forged
	// one can only 404.
	if r.FormValue("to") == "podium" {
		if code := strings.TrimSpace(r.FormValue("code")); code != "" {
			return "/podium/" + url.PathEscape(code)
		}
		return "/podium"
	}
	raw := strings.TrimPrefix(r.FormValue("back"), "?")
	if raw == "" {
		return "/"
	}
	submitted, err := url.ParseQuery(raw)
	if err != nil {
		return "/"
	}
	keep := url.Values{}
	for _, key := range boardParams {
		if v := strings.TrimSpace(submitted.Get(key)); v != "" {
			keep.Set(key, v)
		}
	}
	if len(keep) == 0 {
		return "/"
	}
	return "/?" + keep.Encode()
}

// flashBack sets the flash and redirects back, anchored to the film so a long
// board keeps its scroll position.
func (s *Server) flashBack(w http.ResponseWriter, r *http.Request, code string, movieID int64) {
	s.setFlash(w, code)
	target := returnTarget(r)
	if movieID > 0 {
		target += "#film-" + strconv.FormatInt(movieID, 10)
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}
