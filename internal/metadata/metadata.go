// Package metadata looks films up by IMDb id. TMDb is the main source, OMDb
// adds the Rotten Tomatoes and Metacritic scores.
package metadata

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Rating sources.
const (
	SourceIMDb           = "imdb"
	SourceRottenTomatoes = "rotten_tomatoes"
	SourceMetacritic     = "metacritic"
	SourceTMDB           = "tmdb"
)

// Title kinds, needed to label a series or refuse one.
const (
	KindMovie  = "movie"
	KindSeries = "series"
)

type Rating struct {
	Source    string
	Score     float64 // Always 0-100
	VoteCount int
}

type Result struct {
	Title      string
	Year       int
	RuntimeMin int
	Synopsis   string
	PosterURL  string // Remote, downloaded by the caller
	Genres     []string
	Ratings    []Rating
	Kind       string // KindMovie | KindSeries
}

type Service struct {
	tmdb *tmdbClient
	omdb *omdbClient
}

func New(tmdbKey, omdbKey string, hc *http.Client) *Service {
	if hc == nil {
		hc = &http.Client{Timeout: 10 * time.Second}
	}
	s := &Service{}
	if k := strings.TrimSpace(tmdbKey); k != "" {
		s.tmdb = &tmdbClient{key: k, http: hc, base: tmdbBase, images: tmdbImageBase}
	}
	if k := strings.TrimSpace(omdbKey); k != "" {
		s.omdb = &omdbClient{key: k, http: hc, base: omdbBase}
	}
	return s
}

func (s *Service) Enabled() bool { return s != nil && (s.tmdb != nil || s.omdb != nil) }

func (s *Service) Sources() string {
	switch {
	case s == nil:
		return "none"
	case s.tmdb != nil && s.omdb != nil:
		return "TMDb + OMDb"
	case s.tmdb != nil:
		return "TMDb"
	case s.omdb != nil:
		return "OMDb"
	}
	return "none"
}

// Lookup asks both sources about a film. If one fails, the other's result is
// still returned along with the error.
func (s *Service) Lookup(ctx context.Context, imdbID string) (*Result, error) {
	var out Result
	var firstErr error

	if s.tmdb != nil {
		res, err := s.tmdb.lookup(ctx, imdbID)
		if err != nil {
			firstErr = err
		} else if res != nil {
			out = *res
		}
	}

	if s.omdb != nil {
		res, err := s.omdb.lookup(ctx, imdbID)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
		} else if res != nil {
			// TMDb wins on titles and text, OMDb only adds ratings.
			out.Ratings = append(out.Ratings, res.Ratings...)
			fillBlanks(&out, res)
		}
	}

	if out.Title == "" && len(out.Ratings) == 0 {
		return nil, firstErr
	}
	// Partial result: one source worked, the other didn't.
	return &out, firstErr
}

func fillBlanks(dst *Result, src *Result) {
	if dst.Title == "" {
		dst.Title = src.Title
	}
	if dst.Year == 0 {
		dst.Year = src.Year
	}
	if dst.RuntimeMin == 0 {
		dst.RuntimeMin = src.RuntimeMin
	}
	if dst.Synopsis == "" {
		dst.Synopsis = src.Synopsis
	}
	if dst.PosterURL == "" {
		dst.PosterURL = src.PosterURL
	}
	if len(dst.Genres) == 0 {
		dst.Genres = src.Genres
	}
}

// parseScore converts a score in any of the sources' formats to 0-100.
func parseScore(raw string) (float64, bool) {
	raw = strings.TrimSpace(raw)
	switch {
	case raw == "" || strings.EqualFold(raw, "n/a"):
		return 0, false
	case strings.HasSuffix(raw, "%"):
		v, err := strconv.ParseFloat(strings.TrimSuffix(raw, "%"), 64)
		return v, err == nil
	case strings.HasSuffix(raw, "/10"):
		v, err := strconv.ParseFloat(strings.TrimSuffix(raw, "/10"), 64)
		return v * 10, err == nil
	case strings.HasSuffix(raw, "/100"):
		v, err := strconv.ParseFloat(strings.TrimSuffix(raw, "/100"), 64)
		return v, err == nil
	}
	return 0, false
}

func year(raw string) int {
	if len(raw) < 4 {
		return 0
	}
	v, err := strconv.Atoi(raw[:4])
	if err != nil {
		return 0
	}
	return v
}

// Verify tries each API key once at startup, so a bad key shows up in the log.
func (s *Service) Verify(ctx context.Context) []error {
	var errs []error
	if s.tmdb != nil {
		if err := s.tmdb.get(ctx, "/configuration", nil, &struct{}{}); err != nil {
			errs = append(errs, fmt.Errorf("TMDB_API_KEY rejected: %w", err))
		}
	}
	if s.omdb != nil {
		// Any id works, this only checks the key
		if _, err := s.omdb.lookup(ctx, "tt0111161"); err != nil {
			errs = append(errs, fmt.Errorf("OMDB_API_KEY rejected: %w", err))
		}
	}
	return errs
}

// Kind looks up whether an id is a movie or a series, so the series switch can
// be checked before the film is added. An empty kind means unknown.
func (s *Service) Kind(ctx context.Context, imdbID string) (string, error) {
	if s.tmdb != nil {
		kind, _, err := s.tmdb.find(ctx, imdbID)
		return kind, err
	}
	if s.omdb != nil {
		// OMDb has no cheap way to ask, so this is a full lookup.
		res, err := s.omdb.lookup(ctx, imdbID)
		if err != nil || res == nil {
			return "", err
		}
		return res.Kind, nil
	}
	return "", nil
}
