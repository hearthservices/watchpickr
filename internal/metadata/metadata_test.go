package metadata

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestParseScore(t *testing.T) {
	tests := []struct {
		in   string
		want float64
		ok   bool
	}{
		{"9.3/10", 93, true},
		{"91%", 91, true},
		{"80/100", 80, true},
		{"N/A", 0, false},
		{"", 0, false},
		{"rubbish", 0, false},
	}
	for _, tt := range tests {
		got, ok := parseScore(tt.in)
		if got != tt.want || ok != tt.ok {
			t.Errorf("parseScore(%q) = %v, %v; want %v, %v", tt.in, got, ok, tt.want, tt.ok)
		}
	}
}

func TestOMDbReportsFailureInsideA200(t *testing.T) {
	// OMDb answers 200 with Response:"False" when it has nothing.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"Response":"False","Error":"Incorrect IMDb ID."}`))
	}))
	defer ts.Close()

	c := &omdbClient{key: "k", http: ts.Client(), base: ts.URL}
	if _, err := c.lookup(context.Background(), "tt0000000"); err == nil {
		t.Fatal("expected an error for Response:False, got nil")
	}
}

func TestOMDbNormalisesRatings(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{
			"Response":"True","Title":"The Shawshank Redemption","Year":"1994",
			"Runtime":"142 min","Plot":"Hope.","Poster":"http://img/p.jpg",
			"Genre":"Drama, Crime","imdbVotes":"2,900,000",
			"Ratings":[
				{"Source":"Internet Movie Database","Value":"9.3/10"},
				{"Source":"Rotten Tomatoes","Value":"91%"},
				{"Source":"Metacritic","Value":"82/100"}]}`))
	}))
	defer ts.Close()

	c := &omdbClient{key: "k", http: ts.Client(), base: ts.URL}
	res, err := c.lookup(context.Background(), "tt0111161")
	if err != nil {
		t.Fatal(err)
	}
	if res.Year != 1994 || res.RuntimeMin != 142 {
		t.Errorf("year/runtime = %d/%d, want 1994/142", res.Year, res.RuntimeMin)
	}
	if len(res.Genres) != 2 {
		t.Errorf("genres = %v, want two", res.Genres)
	}
	want := map[string]float64{SourceIMDb: 93, SourceRottenTomatoes: 91, SourceMetacritic: 82}
	if len(res.Ratings) != 3 {
		t.Fatalf("ratings = %v", res.Ratings)
	}
	for _, r := range res.Ratings {
		if want[r.Source] != r.Score {
			t.Errorf("%s = %v, want %v", r.Source, r.Score, want[r.Source])
		}
	}
	if res.Ratings[0].VoteCount != 2900000 {
		t.Errorf("imdb votes = %d, want 2900000", res.Ratings[0].VoteCount)
	}
}

func TestDisabledServiceIsUsable(t *testing.T) {
	s := New("", "", nil)
	if s.Enabled() {
		t.Error("a service with no keys must report itself disabled")
	}
	if s.Sources() != "none" {
		t.Errorf("Sources() = %q", s.Sources())
	}
}
