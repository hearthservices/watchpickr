package metadata

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
)

const (
	tmdbBase      = "https://api.themoviedb.org/3"
	tmdbImageBase = "https://image.tmdb.org/t/p/w500"
)

type tmdbClient struct {
	key    string
	http   *http.Client
	base   string // Overridden in tests
	images string
}

// lookup uses /find to get the TMDb id and kind, then /movie or /tv for the
// runtime and genres.
func (c *tmdbClient) lookup(ctx context.Context, imdbID string) (*Result, error) {
	kind, id, err := c.find(ctx, imdbID)
	if err != nil || kind == "" {
		// No match on TMDb
		return nil, err
	}
	return c.detail(ctx, kind, id)
}

// find maps an IMDb id to a TMDb id and kind. Series are only in tv_results.
func (c *tmdbClient) find(ctx context.Context, imdbID string) (kind string, id int64, err error) {
	var found struct {
		MovieResults []struct {
			ID int64 `json:"id"`
		} `json:"movie_results"`
		TVResults []struct {
			ID int64 `json:"id"`
		} `json:"tv_results"`
	}

	if err := c.get(ctx, "/find/"+url.PathEscape(imdbID),
		url.Values{"external_source": {"imdb_id"}}, &found); err != nil {
		return "", 0, err
	}
	switch {
	case len(found.MovieResults) > 0:
		return KindMovie, found.MovieResults[0].ID, nil
	case len(found.TVResults) > 0:
		return KindSeries, found.TVResults[0].ID, nil
	}
	return "", 0, nil
}

// tmdbDetail is used for both /movie and /tv, fields missing from one are left
// empty.
type tmdbDetail struct {
	// Films
	Title       string `json:"title"`
	ReleaseDate string `json:"release_date"`
	Runtime     int    `json:"runtime"`

	// Series

	Name           string `json:"name"`
	FirstAirDate   string `json:"first_air_date"`
	EpisodeRunTime []int  `json:"episode_run_time"`

	// Both

	Overview    string  `json:"overview"`
	PosterPath  string  `json:"poster_path"`
	VoteAverage float64 `json:"vote_average"`
	VoteCount   int     `json:"vote_count"`
	Genres      []struct {
		Name string `json:"name"`
	} `json:"genres"`
}

func (c *tmdbClient) detail(ctx context.Context, kind string, id int64) (*Result, error) {
	endpoint := "/movie/"
	if kind == KindSeries {
		endpoint = "/tv/"
	}

	var d tmdbDetail
	if err := c.get(ctx, fmt.Sprintf("%s%d", endpoint, id), nil, &d); err != nil {
		return nil, err
	}

	out := &Result{Kind: kind, Synopsis: d.Overview}
	if kind == KindSeries {
		out.Title = d.Name
		out.Year = year(d.FirstAirDate)
		if len(d.EpisodeRunTime) > 0 {
			out.RuntimeMin = d.EpisodeRunTime[0] // Per episode
		}
	} else {
		out.Title = d.Title
		out.Year = year(d.ReleaseDate)
		out.RuntimeMin = d.Runtime
	}

	if d.PosterPath != "" {
		out.PosterURL = c.images + d.PosterPath
	}
	for _, g := range d.Genres {
		out.Genres = append(out.Genres, g.Name)
	}

	// Only used without OMDb, which has the real IMDb score.
	if d.VoteAverage > 0 {
		out.Ratings = append(out.Ratings, Rating{
			Source: SourceTMDB, Score: d.VoteAverage * 10, VoteCount: d.VoteCount,
		})
	}
	return out, nil
}

func (c *tmdbClient) get(ctx context.Context, path string, q url.Values, into any) error {
	if q == nil {
		q = url.Values{}
	}
	q.Set("api_key", c.key)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path+"?"+q.Encode(), nil)
	if err != nil {
		return err
	}
	res, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("tmdb %s: %w", path, err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("tmdb %s: %s", path, res.Status)
	}
	if err := json.NewDecoder(res.Body).Decode(into); err != nil {
		return fmt.Errorf("tmdb %s: decode: %w", path, err)
	}
	return nil
}
