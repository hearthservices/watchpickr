// Package browse filters and sorts the board. Filters are URL parameters, so a
// filtered view can be shared as a link.
package browse

import (
	"cmp"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/hearthservices/watchpickr/internal/store"
)

// Sort keys accepted in the query string
const (
	SortVotes  = "votes" // Default
	SortRating = "rating"
	SortYear   = "year"
	SortNew    = "new"
	SortTitle  = "title"
)

const (
	ShowOpen    = "open" // Default
	ShowAll     = "all"
	ShowWatched = "watched"
)

type Query struct {
	Text      string  // Title, synopsis, genre or who suggested it
	Genre     string  // Exact, as offered by Genres
	Kind      string  // "movie" or "series", empty means both
	MinRating float64 // 0-100, 0 means no minimum
	Sort      string
	Show      string
}

// ParseQuery reads a Query from URL values. Anything missing or unknown falls
// back to the default.
func ParseQuery(values url.Values) Query {
	q := Query{
		Text:  strings.TrimSpace(values.Get("q")),
		Genre: strings.TrimSpace(values.Get("genre")),
		Kind:  strings.ToLower(strings.TrimSpace(values.Get("kind"))),
		Sort:  strings.ToLower(strings.TrimSpace(values.Get("sort"))),
		Show:  strings.ToLower(strings.TrimSpace(values.Get("show"))),
	}

	// Accept 7,5 as well as 7.5
	raw := strings.Replace(values.Get("min_rating"), ",", ".", 1)
	if v, err := strconv.ParseFloat(raw, 64); err == nil && v > 0 && v <= 100 {
		q.MinRating = v
	}

	switch q.Sort {
	case SortVotes, SortRating, SortYear, SortNew, SortTitle:
	default:
		q.Sort = SortVotes
	}

	switch q.Show {
	case ShowOpen, ShowAll, ShowWatched:
	default:
		q.Show = ShowOpen
	}
	return q
}

// Active reports whether any filter is set. The podium is hidden when it is.
func (q Query) Active() bool {
	return q.Text != "" || q.FilterCount() > 0
}

// FilterCount is how many of the filters are in use, minus search since it's always visible.
func (q Query) FilterCount() int {
	n := 0
	for _, set := range []bool{
		q.Genre != "", q.Kind != "", q.MinRating > 0, q.Sort != SortVotes, q.Show != ShowOpen,
	} {
		if set {
			n++
		}
	}
	return n
}

// MinRatingLabel renders the rating filter without trailing zeroes.
func (q Query) MinRatingLabel() string {
	if q.MinRating == 0 {
		return ""
	}
	return strconv.FormatFloat(q.MinRating, 'f', -1, 64)
}

func Apply(movies []store.Movie, q Query) []store.Movie {
	out := make([]store.Movie, 0, len(movies))
	for _, m := range movies {
		if matches(m, q) {
			out = append(out, m)
		}
	}
	sortMovies(out, q.Sort)
	return out
}

func matches(m store.Movie, q Query) bool {
	switch q.Show {
	case ShowOpen:
		if m.Watched() {
			return false
		}
	case ShowWatched:
		if !m.Watched() {
			return false
		}
	}
	if q.Kind != "" && m.Kind != q.Kind {
		return false
	}
	if q.Genre != "" && !slices.Contains(m.Genres, q.Genre) {
		return false
	}
	if q.MinRating > 0 {
		score, ok := m.Score()
		if !ok || score < q.MinRating {
			return false
		}
	}

	return q.Text == "" || matchesText(m, q.Text)
}

// matchesText requires every word to match somewhere, so more words narrow the
// result.
func matchesText(m store.Movie, text string) bool {
	hay := strings.ToLower(strings.Join([]string{
		m.Title, m.Synopsis, m.AddedBy, strings.Join(m.Genres, " "),
	}, " "))
	for _, word := range strings.Fields(strings.ToLower(text)) {
		if !strings.Contains(hay, word) {
			return false
		}
	}
	return true
}

func sortMovies(movies []store.Movie, sort string) {
	switch sort {
	case SortRating:
		slices.SortStableFunc(movies, func(a, b store.Movie) int {
			as, aok := a.Score()
			bs, bok := b.Score()
			switch {
			case aok && bok:
				return cmp.Compare(bs, as) // Highest first
			case aok:
				return -1 // Rated before unrated
			case bok:
				return 1
			}
			return 0
		})
	case SortYear:
		slices.SortStableFunc(movies, func(a, b store.Movie) int {
			return cmp.Compare(b.Year, a.Year)
		})
	case SortNew:
		slices.SortStableFunc(movies, func(a, b store.Movie) int {
			return b.CreatedAt.Compare(a.CreatedAt)
		})
	case SortTitle:
		slices.SortStableFunc(movies, func(a, b store.Movie) int {
			return cmp.Compare(strings.ToLower(a.Title), strings.ToLower(b.Title))
		})
	}

	// SortVotes needs nothing, the store already returns that order. The sorts
	// above are stable, so ties keep the store's order.
}

// Genres lists the genres on the board, so the dropdown only offers ones that
// match something.
func Genres(movies []store.Movie) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range movies {
		for _, g := range m.Genres {
			if !seen[g] {
				seen[g] = true
				out = append(out, g)
			}
		}
	}
	slices.Sort(out)
	return out
}
