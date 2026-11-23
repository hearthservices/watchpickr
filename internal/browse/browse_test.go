package browse

import (
	"testing"
	"time"

	"github.com/hearthservices/watchpickr/internal/store"
)

func board() []store.Movie {
	now := time.Now()
	return []store.Movie{
		{
			ID: 1, Title: "Heat", Kind: "movie", Year: 1995, Votes: 3, CreatedAt: now.Add(-3 * time.Hour),
			Genres: []string{"Crime", "Drama"}, Ratings: []store.Rating{{Source: "imdb", Score: 84}},
		},
		{
			ID: 2, Title: "The Gentlemen", Kind: "series", Year: 2024, Votes: 3, CreatedAt: now.Add(-2 * time.Hour),
			Genres: []string{"Crime"}, Ratings: []store.Rating{{Source: "imdb", Score: 80}},
		},
		{
			ID: 3, Title: "Paddington", Kind: "movie", Year: 2014, Votes: 1, CreatedAt: now.Add(-time.Hour),
			Genres: []string{"Family"},
		},
		{
			ID: 4, Title: "Old Watched Thing", Kind: "movie", Year: 1980, CreatedAt: now,
			WatchedAt: now, Genres: []string{"Drama"},
		},
	}
}

func titles(ms []store.Movie) []string {
	out := make([]string, len(ms))
	for i, m := range ms {
		out[i] = m.Title
	}
	return out
}

func TestShowHidesWatchedByDefault(t *testing.T) {
	got := titles(Apply(board(), ParseQuery(nil)))
	for _, title := range got {
		if title == "Old Watched Thing" {
			t.Fatalf("watched title leaked into the default view: %v", got)
		}
	}
}

func TestFilters(t *testing.T) {
	tests := []struct {
		name  string
		query map[string][]string
		want  []string
	}{
		{"by kind", map[string][]string{"kind": {"series"}}, []string{"The Gentlemen"}},
		{"by genre", map[string][]string{"genre": {"Crime"}}, []string{"Heat", "The Gentlemen"}},
		{"by rating", map[string][]string{"min_rating": {"82"}}, []string{"Heat"}},
		{"free text", map[string][]string{"q": {"crime drama"}}, []string{"Heat"}},
		{"watched only", map[string][]string{"show": {"watched"}}, []string{"Old Watched Thing"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := titles(Apply(board(), ParseQuery(tt.query)))
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("got %v, want %v", got, tt.want)
				}
			}
		})
	}
}

func TestSortsAreStableOnTheStoreOrder(t *testing.T) {
	// Heat and The Gentlemen both have three votes. By year the newer one comes
	// first, sorts they tie on keep the store's order.
	byYear := titles(Apply(board(), ParseQuery(map[string][]string{"sort": {"year"}})))
	if byYear[0] != "The Gentlemen" {
		t.Errorf("sort=year gave %v", byYear)
	}

	unrated := Apply(board(), ParseQuery(map[string][]string{"sort": {"rating"}}))
	if unrated[len(unrated)-1].Title != "Paddington" {
		t.Errorf("an unrated title must sink to the bottom, got %v", titles(unrated))
	}
}

func TestFilterCountLeavesSearchOut(t *testing.T) {
	q := ParseQuery(map[string][]string{"q": {"heat"}, "genre": {"Crime"}, "sort": {"year"}})
	if got := q.FilterCount(); got != 2 {
		t.Errorf("FilterCount = %d, want 2 for a genre and a sort", got)
	}
	if !ParseQuery(map[string][]string{"q": {"heat"}}).Active() {
		t.Error("a search on its own must count as filtering")
	}
}

func TestActive(t *testing.T) {
	if ParseQuery(nil).Active() {
		t.Error("an empty query must not count as filtering")
	}
	if !ParseQuery(map[string][]string{"genre": {"Crime"}}).Active() {
		t.Error("a genre filter must count as filtering")
	}
	if ParseQuery(map[string][]string{"sort": {"nonsense"}}).Active() {
		t.Error("an unrecognised sort falls back to the default and is not filtering")
	}
}
