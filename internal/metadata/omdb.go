package metadata

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

const omdbBase = "https://www.omdbapi.com/"

type omdbClient struct {
	key  string
	http *http.Client
	base string
}

func (c *omdbClient) lookup(ctx context.Context, imdbID string) (*Result, error) {
	q := url.Values{"apikey": {c.key}, "i": {imdbID}, "plot": {"short"}}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	res, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("omdb: %w", err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("omdb: %s", res.Status)
	}

	var body struct {
		Response  string `json:"Response"` // OMDb returns a 200 with "False" on failure for some reason...
		Error     string `json:"Error"`
		Title     string `json:"Title"`
		Year      string `json:"Year"`
		Runtime   string `json:"Runtime"`
		Plot      string `json:"Plot"`
		Poster    string `json:"Poster"`
		Genre     string `json:"Genre"`
		IMDbVotes string `json:"imdbVotes"`
		Type      string `json:"Type"`
		Ratings   []struct {
			Source string `json:"Source"`
			Value  string `json:"Value"`
		} `json:"Ratings"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("omdb: decode: %w", err)
	}
	// A 200 with Response:"False" is OMDb's way of saying "no such film found".
	// (if only there was a dedicated http status code for that...)
	if !strings.EqualFold(body.Response, "True") {
		return nil, fmt.Errorf("omdb: %s", naOr(body.Error, "not found"))
	}

	out := &Result{
		Title:      naOr(body.Title, ""),
		Year:       year(naOr(body.Year, "")),
		RuntimeMin: minutes(body.Runtime),
		Synopsis:   naOr(body.Plot, ""),
		PosterURL:  naOr(body.Poster, ""),
	}

	if strings.EqualFold(body.Type, "series") {
		out.Kind = KindSeries
	} else {
		out.Kind = KindMovie
	}

	for _, g := range strings.Split(naOr(body.Genre, ""), ",") {
		if g = strings.TrimSpace(g); g != "" {
			out.Genres = append(out.Genres, g)
		}
	}

	votes := count(body.IMDbVotes)
	for _, r := range body.Ratings {
		score, ok := parseScore(r.Value)
		if !ok {
			continue
		}
		switch r.Source {
		case "Internet Movie Database":
			out.Ratings = append(out.Ratings, Rating{SourceIMDb, score, votes})
		case "Rotten Tomatoes":
			out.Ratings = append(out.Ratings, Rating{SourceRottenTomatoes, score, 0})
		case "Metacritic":
			out.Ratings = append(out.Ratings, Rating{SourceMetacritic, score, 0})
		}
	}
	return out, nil
}

// naOr replaces OMDb's "N/A" placeholder, a literal "N/A" on the board just
// doesn't look nice.
func naOr(v, fallback string) string {
	v = strings.TrimSpace(v)
	if v == "" || strings.EqualFold(v, "n/a") {
		return fallback
	}
	return v
}

func minutes(raw string) int {
	n, err := strconv.Atoi(strings.TrimSpace(strings.TrimSuffix(naOr(raw, ""), " min")))
	if err != nil {
		return 0
	}
	return n
}

func count(raw string) int {
	n, err := strconv.Atoi(strings.ReplaceAll(naOr(raw, "0"), ",", ""))
	if err != nil {
		return 0
	}
	return n
}
