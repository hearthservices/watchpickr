// Package demo seeds a throwaway database for -demo.
package demo

import (
	"context"
	"fmt"
	"time"

	"github.com/hearthservices/watchpickr/internal/auth"
	"github.com/hearthservices/watchpickr/internal/poster"
	"github.com/hearthservices/watchpickr/internal/store"
)

// Password is used by every demo account.
const Password = "watchpickr"

// JoinCode is fixed so the README can mention it. The join alphabet has no I,
// L or O, hence the funky spelling.
const JoinCode = "FRYDAY"

// posterBase is TMDb's image server, which needs no API key. Same size as the
// real lookups.
const posterBase = "https://image.tmdb.org/t/p/w500"

const (
	movie  = "movie"
	series = "series"

	standingVotes = 3
	sessionVotes  = 2
)

type film struct {
	imdbID   string
	title    string
	year     int
	runtime  int
	kind     string
	genres   []string
	synopsis string
	imdb     float64
	tmdb     float64
	poster   string // Path on TMDb's image server
}

var films = []film{
	{
		"tt6710474", "Everything Everywhere All at Once", 2022, 139, movie,
		[]string{"Action", "Adventure", "Comedy"},
		"A laundromat owner is swept into an adventure across parallel universes.", 79, 78,
		"/w3LxiVYdWWRvEVdn5RYq6jIqkb1.jpg",
	},
	{
		"tt2543164", "Arrival", 2016, 116, movie,
		[]string{"Drama", "Mystery", "Sci-Fi"},
		"A linguist is recruited to speak with visitors who have landed across the world.", 79, 76,
		"/pEzNVQfdzYDzVK0XqxERIw2x2se.jpg",
	},
	{
		"tt6751668", "Parasite", 2019, 132, movie,
		[]string{"Drama", "Thriller"},
		"A struggling family schemes their way into the household of a wealthy one.", 85, 85,
		"/7IiTTgloJzvGI1TAYymCfbfl3vT.jpg",
	},
	{
		"tt0111161", "The Shawshank Redemption", 1994, 142, movie,
		[]string{"Drama"},
		"A banker sentenced for a murder he did not commit builds a life inside Shawshank.", 93, 87,
		"/9cqNxx0GxF0bflZmeSMuL5tnGzr.jpg",
	},
	{
		"tt0068646", "The Godfather", 1972, 175, movie,
		[]string{"Crime", "Drama"},
		"An ageing patriarch hands his crime dynasty to a reluctant youngest son.", 92, 87,
		"/3bhkrj58Vtu7enYsRolD1fZdja1.jpg",
	},
	{
		"tt0110912", "Pulp Fiction", 1994, 154, movie,
		[]string{"Crime", "Drama"},
		"Two hitmen, a boxer and a gangster's wife cross paths over a few violent days.", 89, 86,
		"/d5iIlFn5s0ImszYzBPb8JPIfbXD.jpg",
	},
	{
		"tt1375666", "Inception", 2010, 148, movie,
		[]string{"Action", "Sci-Fi", "Thriller"},
		"A thief who steals secrets from dreams is asked to plant one instead.", 88, 84,
		"/9gk7adHYeDvHkCSEqAvQNLV5Uge.jpg",
	},
	{
		"tt0816692", "Interstellar", 2014, 169, movie,
		[]string{"Adventure", "Drama", "Sci-Fi"},
		"A former pilot leads a mission through a wormhole to find humanity a new home.", 87, 86,
		"/yQvGrMoipbRoddT0ZR8tPoR7NfX.jpg",
	},
	{
		"tt0903747", "Breaking Bad", 2008, 49, series,
		[]string{"Crime", "Drama", "Thriller"},
		"A chemistry teacher with a terminal diagnosis turns to manufacturing drugs.", 95, 89,
		"/ztkUQFLlC19CCMYHW9o1zWhJRNq.jpg",
	},
	{
		"tt0417299", "Avatar: The Last Airbender", 2005, 23, series,
		[]string{"Animation", "Action", "Adventure"},
		"A young airbender wakes after a century to find the world at war.", 91, 86,
		"/9RQhVb3r3mCMqYVhLoCu4EvuipP.jpg",
	},
}

// guestFilm is suggested by a guest, so only the open session has it.
var guestFilm = film{
	"tt0245429", "Spirited Away", 2001, 125, movie,
	[]string{"Animation", "Family", "Fantasy"},
	"A girl wanders into a world of spirits and works in a bathhouse to free her parents.", 86, 85,
	"/39wmItIWsg5sZMyRUHLkWBcuVCM.jpg",
}

func (f film) metadata() store.Metadata {
	return store.Metadata{
		Title:      f.title,
		Year:       f.year,
		RuntimeMin: f.runtime,
		Synopsis:   f.synopsis,
		Kind:       f.kind,
		Genres:     f.genres,
		Ratings: []store.Rating{
			{Source: "imdb", Score: f.imdb},
			{Source: "tmdb", Score: f.tmdb},
		},
	}
}

// members are the seeded accounts, in the order they suggest films.
var members = []string{"Anna", "Benny", "Cecilia", "David"}

type ballot struct {
	imdbID string
	voters []string
}

// standingBallots spend every member's three votes. The Godfather gets marked
// watched afterwards, which gives Anna and Cecilia their votes back.
var standingBallots = []ballot{
	{"tt6710474", []string{"Anna", "Benny", "David"}},
	{"tt2543164", []string{"Benny", "David"}},
	{"tt6751668", []string{"Anna", "Cecilia"}},
	{"tt0111161", []string{"Benny"}},
	{"tt0903747", []string{"Cecilia"}},
	{"tt0417299", []string{"David"}},
	{"tt0068646", []string{"Anna", "Cecilia"}},
}

// Seed fills a fresh store. The admin account is created by the caller.
func Seed(ctx context.Context, st *store.Store, posters *poster.Cache) error {
	hash, err := auth.HashPassword(Password)
	if err != nil {
		return err
	}

	people := make(map[string]*store.User, len(members))
	for _, name := range members {
		u, err := st.CreateUser(ctx, name, hash, false)
		if err != nil {
			return fmt.Errorf("seed member %s: %w", name, err)
		}
		people[name] = u
	}

	standing, err := st.StandingPoll(ctx)
	if err != nil {
		return err
	}

	if _, err := st.SetMaxVotes(ctx, standing.ID, standingVotes, store.PolicyTrim); err != nil {
		return err
	}

	ids := make(map[string]int64, len(films))
	for i, f := range films {
		suggester := people[members[i%len(members)]]
		id, err := st.AddMovie(ctx, f.imdbID, f.title, suggester.ID)
		if err != nil {
			return fmt.Errorf("seed film %s: %w", f.title, err)
		}
		if err := st.SaveMetadata(ctx, id, f.metadata()); err != nil {
			return fmt.Errorf("seed metadata %s: %w", f.title, err)
		}
		ids[f.imdbID] = id
	}

	for _, b := range standingBallots {
		for _, name := range b.voters {
			if err := st.Vote(ctx, standing.ID, people[name].ID, ids[b.imdbID], standingVotes); err != nil {
				return fmt.Errorf("seed vote for %s by %s: %w", b.imdbID, name, err)
			}
		}
	}
	if err := st.MarkWatched(ctx, standing.ID, ids["tt0068646"]); err != nil {
		return fmt.Errorf("seed screening: %w", err)
	}

	friday, err := seedSession(ctx, st, "Friday film night", JoinCode,
		map[string][]string{
			"Erik":  {"tt6710474", "tt0110912"},
			"Frida": {"tt6751668", "tt6710474"},
		}, ids, false)
	if err != nil {
		return err
	}
	if err := seedGuestFilm(ctx, st, friday, "Hanna", guestFilm, ids); err != nil {
		return err
	}

	closedCode, err := auth.JoinCode()
	if err != nil {
		return err
	}
	saturday, err := seedSession(ctx, st, "Last Saturday", closedCode,
		map[string][]string{"Gustav": {"tt0816692", "tt1375666"}}, ids, true)
	if err != nil {
		return err
	}
	// The main board only gets a note about this one.
	if err := st.MarkWatched(ctx, saturday.ID, ids["tt0816692"]); err != nil {
		return fmt.Errorf("seed session screening: %w", err)
	}

	return addPosters(ctx, st, posters, ids)
}

// seedSession creates a session board with its own guests and votes.
func seedSession(ctx context.Context, st *store.Store, name, code string,
	votes map[string][]string, ids map[string]int64, closed bool,
) (*store.Poll, error) {
	poll, err := st.CreateSessionPoll(ctx, name, sessionVotes, code)
	if err != nil {
		return nil, fmt.Errorf("seed session %s: %w", name, err)
	}
	for guest, picks := range votes {
		u, err := st.CreateGuest(ctx, poll.ID, guest)
		if err != nil {
			return nil, fmt.Errorf("seed guest %s: %w", guest, err)
		}
		for _, imdbID := range picks {
			if err := st.Vote(ctx, poll.ID, u.ID, ids[imdbID], sessionVotes); err != nil {
				return nil, fmt.Errorf("seed guest vote %s: %w", imdbID, err)
			}
		}
	}
	if closed {
		return poll, st.ClosePoll(ctx, poll.ID)
	}
	return poll, nil
}

// seedGuestFilm has a new guest suggest a film and vote for it.
func seedGuestFilm(ctx context.Context, st *store.Store, poll *store.Poll, guest string, f film, ids map[string]int64) error {
	u, err := st.CreateGuest(ctx, poll.ID, guest)
	if err != nil {
		return fmt.Errorf("seed guest %s: %w", guest, err)
	}
	id, err := st.AddSessionMovie(ctx, poll.ID, f.imdbID, f.title, u.ID)
	if err != nil {
		return fmt.Errorf("seed guest film %s: %w", f.title, err)
	}
	if err := st.SaveMetadata(ctx, id, f.metadata()); err != nil {
		return fmt.Errorf("seed metadata %s: %w", f.title, err)
	}
	ids[f.imdbID] = id
	return st.Vote(ctx, poll.ID, u.ID, id, sessionVotes)
}

// addPosters downloads the posters from TMDb. The board works without them, so
// a failed download is skipped, and offline it gives up after a few seconds.
func addPosters(ctx context.Context, st *store.Store, posters *poster.Cache, ids map[string]int64) error {
	fetch, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	for _, f := range append([]film{guestFilm}, films...) {
		file, err := posters.Save(fetch, f.imdbID, posterBase+f.poster)
		if err != nil {
			continue
		}
		if err := st.SaveMetadata(ctx, ids[f.imdbID], store.Metadata{PosterFile: file}); err != nil {
			return fmt.Errorf("seed poster %s: %w", f.title, err)
		}
	}
	return nil
}
