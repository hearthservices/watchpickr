package store

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/hearthservices/watchpickr/internal/auth"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestDuplicateUsername(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	if _, err := st.CreateUser(ctx, "Jb", "hash", false); err != nil {
		t.Fatalf("first create: %v", err)
	}
	// Usernames are case-insensitive
	_, err := st.CreateUser(ctx, "jb", "hash", false)
	if !errors.Is(err, ErrDuplicateUser) {
		t.Fatalf("second create err = %v, want ErrDuplicateUser "+
			"(if this is a raw SQL error, print se.Code() in isUniqueViolation "+
			"and add the constant it reports)", err)
	}
}

func TestAuthenticateRejectsUnknownAndWrong(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	hash := "$2a$10$abcdefghijklmnopqrstuv" // Not a valid bcrypt hash
	if _, err := st.CreateUser(ctx, "jb", hash, false); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Authenticate(ctx, "nobody", "whatever"); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown user err = %v, want ErrNotFound", err)
	}
	if _, err := st.Authenticate(ctx, "jb", "wrong"); !errors.Is(err, ErrNotFound) {
		t.Errorf("wrong password err = %v, want ErrNotFound", err)
	}
}

func TestChangePasswordChecksTheCurrentOne(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	hash, err := auth.HashPassword("first password")
	if err != nil {
		t.Fatal(err)
	}
	u, err := st.CreateUser(ctx, "jb", hash, false)
	if err != nil {
		t.Fatal(err)
	}

	if err := st.ChangePassword(ctx, u.ID, "a wild guess", "second password"); !errors.Is(err, ErrWrongPassword) {
		t.Errorf("wrong current password = %v, want ErrWrongPassword", err)
	}
	if err := st.ChangePassword(ctx, u.ID, "first password", "second password"); err != nil {
		t.Fatalf("change password: %v", err)
	}
	if _, err := st.Authenticate(ctx, "jb", "second password"); err != nil {
		t.Errorf("the new password doesn't work: %v", err)
	}
}

func seedUserAndMovie(t *testing.T, st *Store, name, imdbID string) (userID, movieID int64) {
	t.Helper()
	ctx := context.Background()
	u, err := st.CreateUser(ctx, name, "x", false)
	if err != nil {
		t.Fatal(err)
	}
	m, err := st.AddMovie(ctx, imdbID, "Film "+imdbID, u.ID)
	if err != nil {
		t.Fatal(err)
	}
	return u.ID, m
}

func TestVoteBudgetCannotBeExceeded(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	poll, err := st.StandingPoll(ctx)
	if err != nil {
		t.Fatal(err)
	}

	userID, _ := seedUserAndMovie(t, st, "jb", "tt0000001")
	const max = 2
	var ids []int64
	for i := 2; i <= 4; i++ {
		m, err := st.AddMovie(ctx, fmt.Sprintf("tt000000%d", i), "Film", userID)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, m)
	}

	for i, id := range ids {
		err := st.Vote(ctx, poll.ID, userID, id, max)
		if i < max && err != nil {
			t.Fatalf("vote %d: %v", i, err)
		}
		if i >= max && !errors.Is(err, ErrNoVotesLeft) {
			t.Fatalf("vote %d err = %v, want ErrNoVotesLeft", i, err)
		}
	}
}

func TestCannotVoteTwiceForTheSameFilm(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	poll, err := st.StandingPoll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	userID, movieID := seedUserAndMovie(t, st, "jb", "tt0000001")

	if err := st.Vote(ctx, poll.ID, userID, movieID, 5); err != nil {
		t.Fatal(err)
	}
	if err := st.Vote(ctx, poll.ID, userID, movieID, 5); !errors.Is(err, ErrAlreadyVoted) {
		t.Fatalf("second vote err = %v, want ErrAlreadyVoted", err)
	}
}

func TestMarkWatchedSnapshotsAndRefunds(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	poll, err := st.StandingPoll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	userID, movieID := seedUserAndMovie(t, st, "jb", "tt0000001")

	if err := st.Vote(ctx, poll.ID, userID, movieID, 1); err != nil {
		t.Fatal(err)
	}
	if used, _ := st.VotesUsed(ctx, poll.ID, userID); used != 1 {
		t.Fatalf("used = %d, want 1", used)
	}

	if err := st.MarkWatched(ctx, poll.ID, movieID); err != nil {
		t.Fatal(err)
	}

	// The vote is refunded
	if used, _ := st.VotesUsed(ctx, poll.ID, userID); used != 0 {
		t.Errorf("used after watching = %d, want 0", used)
	}
	// But we still know who voted for it
	voters, err := st.Voters(ctx, poll.ID, movieID)
	if err != nil {
		t.Fatal(err)
	}
	if len(voters) != 1 || voters[0] != "jb" {
		t.Errorf("voters = %v, want [jb]", voters)
	}
	// And it can't be voted for while watched
	if err := st.Vote(ctx, poll.ID, userID, movieID, 1); !errors.Is(err, ErrMovieWatched) {
		t.Errorf("vote on watched film err = %v, want ErrMovieWatched", err)
	}
}

func TestReopenRestoresNobodysVote(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	poll, err := st.StandingPoll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	userID, first := seedUserAndMovie(t, st, "jb", "tt0000001")
	second, err := st.AddMovie(ctx, "tt0000002", "Another", userID)
	if err != nil {
		t.Fatal(err)
	}

	// One vote, spent on the first film, which then gets watched.
	if err := st.Vote(ctx, poll.ID, userID, first, 1); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkWatched(ctx, poll.ID, first); err != nil {
		t.Fatal(err)
	}
	// The refunded vote is spent elsewhere.
	if err := st.Vote(ctx, poll.ID, userID, second, 1); err != nil {
		t.Fatal(err)
	}
	// Reopening must not bring the old vote back
	if err := st.Reopen(ctx, poll.ID, first); err != nil {
		t.Fatal(err)
	}
	if used, _ := st.VotesUsed(ctx, poll.ID, userID); used != 1 {
		t.Errorf("used after reopen = %d, want 1 -- an old vote came back", used)
	}
	// The screening record survives the reopen.
	if voters, _ := st.Voters(ctx, poll.ID, first); len(voters) != 1 {
		t.Errorf("voters after reopen = %v, want the snapshot kept", voters)
	}
}

func TestGuestVotesNeverReachTheStandingBoard(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	standing, err := st.StandingPoll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	session, err := st.CreateSessionPoll(ctx, "Friday", 3, "ABC234")
	if err != nil {
		t.Fatal(err)
	}

	member, err := st.CreateUser(ctx, "jb", "hash", false)
	if err != nil {
		t.Fatal(err)
	}
	guest, err := st.CreateGuest(ctx, session.ID, "jc")
	if err != nil {
		t.Fatal(err)
	}

	// One shared film, voted for from both boards.
	film, err := st.AddMovie(ctx, "tt0111161", "Shawshank", member.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Vote(ctx, standing.ID, member.ID, film, 5); err != nil {
		t.Fatal(err)
	}
	if err := st.Vote(ctx, session.ID, guest.ID, film, 3); err != nil {
		t.Fatal(err)
	}

	// Each board only sees its own vote
	for _, tc := range []struct {
		name   string
		poll   int64
		viewer int64
	}{
		{"standing", standing.ID, member.ID},
		{"session", session.ID, guest.ID},
	} {
		movies, err := st.Movies(ctx, tc.poll, tc.viewer)
		if err != nil {
			t.Fatal(err)
		}
		if len(movies) != 1 || movies[0].Votes != 1 {
			t.Errorf("%s board saw %d votes, want exactly its own", tc.name, movies[0].Votes)
		}
	}

	// And the guest's own poll routes correctly.
	got, err := st.PollForUser(ctx, guest)
	if err != nil || got.ID != session.ID {
		t.Errorf("PollForUser(guest) = %v, %v; want the session", got, err)
	}
	if got, err := st.PollForUser(ctx, member); err != nil || got.ID != standing.ID {
		t.Errorf("PollForUser(member) = %v, %v; want the standing board", got, err)
	}
}

func TestDeletingASessionTakesItsGuestsAndVotes(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	session, _ := st.CreateSessionPoll(ctx, "Friday", 3, "ABC234")
	guest, _ := st.CreateGuest(ctx, session.ID, "jc")
	film, _ := st.AddMovie(ctx, "tt0111161", "Shawshank", guest.ID)
	if err := st.Vote(ctx, session.ID, guest.ID, film, 3); err != nil {
		t.Fatal(err)
	}

	if err := st.DeletePoll(ctx, session.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UserByID(ctx, guest.ID); !errors.Is(err, ErrNotFound) {
		t.Error("the guest survived their session -- is foreign_keys(1) set?")
	}
	// The film is shared, so it stays
	standing, _ := st.StandingPoll(ctx)
	if movies, _ := st.Movies(ctx, standing.ID, 0); len(movies) != 1 {
		t.Error("deleting a session took a film off the shared catalogue")
	}
}

func TestClosedSessionRefusesVotes(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	session, _ := st.CreateSessionPoll(ctx, "Friday", 3, "ABC234")
	guest, _ := st.CreateGuest(ctx, session.ID, "jc")
	film, _ := st.AddMovie(ctx, "tt0111161", "Shawshank", guest.ID)

	if err := st.ClosePoll(ctx, session.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.Vote(ctx, session.ID, guest.ID, film, 3); !errors.Is(err, ErrPollClosed) {
		t.Errorf("vote on a closed board = %v, want ErrPollClosed", err)
	}
}

// sees reports whether a poll's board shows the film.
func sees(t *testing.T, st *Store, pollID, movieID int64) bool {
	t.Helper()
	_, err := st.MovieByID(context.Background(), pollID, 0, movieID)
	if err != nil && !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	return err == nil
}

func TestGuestSuggestionStaysInItsSession(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	standing, _ := st.StandingPoll(ctx)
	friday, _ := st.CreateSessionPoll(ctx, "Friday", 3, "ABC234")
	saturday, _ := st.CreateSessionPoll(ctx, "Saturday", 3, "DEF567")
	member, _ := st.CreateUser(ctx, "jb", "hash", false)
	guest, _ := st.CreateGuest(ctx, friday.ID, "jc")
	other, _ := st.CreateGuest(ctx, saturday.ID, "jd")

	film, err := st.AddSessionMovie(ctx, friday.ID, "tt0245429", "Spirited Away", guest.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !sees(t, st, friday.ID, film) {
		t.Error("the session can't see its own guest's suggestion")
	}
	if sees(t, st, standing.ID, film) || sees(t, st, saturday.ID, film) {
		t.Error("a guest's suggestion leaked out of its session")
	}
	// Not even by sending its id
	if err := st.Vote(ctx, standing.ID, member.ID, film, 5); !errors.Is(err, ErrNotFound) {
		t.Errorf("standing board vote for it = %v, want ErrNotFound", err)
	}

	// Another session gets a link to the same film
	linked, err := st.AddSessionMovie(ctx, saturday.ID, "tt0245429", "Spirited Away", other.ID)
	if err != nil || linked != film {
		t.Errorf("second session got film %d, %v; want film %d", linked, err, film)
	}
	if _, err := st.AddSessionMovie(ctx, friday.ID, "tt0245429", "Spirited Away", guest.ID); !errors.Is(err, ErrDuplicateMovie) {
		t.Errorf("same session again = %v, want ErrDuplicateMovie", err)
	}

	// A member suggesting it shares it with every board, still as one film
	shared, err := st.AddMovie(ctx, "tt0245429", "Spirited Away", member.ID)
	if err != nil || shared != film {
		t.Fatalf("member got film %d, %v; want film %d", shared, err, film)
	}
	if m, err := st.MovieByID(ctx, standing.ID, member.ID, film); err != nil || m.AddedBy != "jb" {
		t.Errorf("standing board has %+v, %v; want it as jb's suggestion", m, err)
	}
	if _, err := st.AddMovie(ctx, "tt0245429", "Spirited Away", member.ID); !errors.Is(err, ErrDuplicateMovie) {
		t.Errorf("suggesting a shared film = %v, want ErrDuplicateMovie", err)
	}
}

func TestDeletingASessionKeepsItsScreenings(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	standing, _ := st.StandingPoll(ctx)
	friday, _ := st.CreateSessionPoll(ctx, "Friday", 3, "ABC234")
	member, _ := st.CreateUser(ctx, "jb", "hash", false)
	guest, _ := st.CreateGuest(ctx, friday.ID, "jc")
	film, _ := st.AddMovie(ctx, "tt0111161", "Shawshank", member.ID)

	if err := st.Vote(ctx, friday.ID, guest.ID, film, 3); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkWatched(ctx, friday.ID, film); err != nil {
		t.Fatal(err)
	}
	if err := st.DeletePoll(ctx, friday.ID); err != nil {
		t.Fatal(err)
	}

	// Still named
	m, err := st.MovieByID(ctx, standing.ID, member.ID, film)
	if err != nil {
		t.Fatal(err)
	}
	if m.WatchedElsewhere == nil || m.WatchedElsewhere.PollName != "Friday" {
		t.Errorf("WatchedElsewhere = %+v, want the deleted session by name", m.WatchedElsewhere)
	}
	// And still showing who backed it, though the guest is gone
	var voter string
	err = st.db.QueryRowContext(ctx, `
SELECT sv.display_name FROM screening_voters sv
JOIN screenings sc ON sc.id = sv.screening_id
WHERE sc.movie_id = ?`, film).Scan(&voter)
	if err != nil || voter != "jc" {
		t.Errorf("voter = %q, %v; want jc", voter, err)
	}
}

func TestScreeningOnAnotherBoardIsOnlyNoted(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	standing, _ := st.StandingPoll(ctx)
	friday, _ := st.CreateSessionPoll(ctx, "Friday", 3, "ABC234")
	member, _ := st.CreateUser(ctx, "jb", "hash", false)
	film, _ := st.AddMovie(ctx, "tt0111161", "Shawshank", member.ID)

	if err := st.Vote(ctx, standing.ID, member.ID, film, 5); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkWatched(ctx, friday.ID, film); err != nil {
		t.Fatal(err)
	}

	m, err := st.MovieByID(ctx, standing.ID, member.ID, film)
	if err != nil {
		t.Fatal(err)
	}
	if m.Watched() || m.Votes != 1 {
		t.Errorf("watched = %v, votes = %d; the session's screening changed this board", m.Watched(), m.Votes)
	}
	if m.WatchedElsewhere == nil || m.WatchedElsewhere.PollName != "Friday" {
		t.Errorf("WatchedElsewhere = %+v, want a note about Friday", m.WatchedElsewhere)
	}

	// Reopened there, so no longer watched there
	if err := st.Reopen(ctx, friday.ID, film); err != nil {
		t.Fatal(err)
	}
	if m, _ := st.MovieByID(ctx, standing.ID, member.ID, film); m.WatchedElsewhere != nil {
		t.Errorf("WatchedElsewhere = %+v after the reopen, want nil", m.WatchedElsewhere)
	}
}

func TestEnsureColumnsAddsWhatIsMissing(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	if _, err := st.db.ExecContext(ctx, `CREATE TABLE old (id INTEGER PRIMARY KEY)`); err != nil {
		t.Fatal(err)
	}
	cols := []column{{"added", "TEXT NOT NULL DEFAULT ''"}}
	if err := st.ensureColumns(ctx, "old", cols); err != nil {
		t.Fatal(err)
	}
	// Runs on every startup, so a second run must do nothing
	if err := st.ensureColumns(ctx, "old", cols); err != nil {
		t.Fatalf("second run: %v", err)
	}
	if _, err := st.db.ExecContext(ctx, `INSERT INTO old (id, added) VALUES (1, 'x')`); err != nil {
		t.Fatalf("column was not added: %v", err)
	}
}

func TestSetMaxVotesPolicies(t *testing.T) {
	setup := func(t *testing.T) (*Store, int64, int64) {
		t.Helper()
		st := newTestStore(t)
		ctx := context.Background()
		poll, err := st.StandingPoll(ctx)
		if err != nil {
			t.Fatal(err)
		}
		u, err := st.CreateUser(ctx, "jb", "hash", false)
		if err != nil {
			t.Fatal(err)
		}
		for i := 1; i <= 3; i++ {
			id, err := st.AddMovie(ctx, fmt.Sprintf("tt000000%d", i), "Film", u.ID)
			if err != nil {
				t.Fatal(err)
			}
			if err := st.Vote(ctx, poll.ID, u.ID, id, 5); err != nil {
				t.Fatal(err)
			}
		}
		return st, poll.ID, u.ID
	}

	t.Run("refuse leaves everything alone", func(t *testing.T) {
		st, pollID, userID := setup(t)
		ctx := context.Background()

		change, err := st.SetMaxVotes(ctx, pollID, 2, PolicyRefuse)
		if !errors.Is(err, ErrOverBudget) {
			t.Fatalf("err = %v, want ErrOverBudget", err)
		}
		if len(change.OverBudget) != 1 || change.OverBudget[0] != "jb" {
			t.Errorf("OverBudget = %v, want [jb]", change.OverBudget)
		}
		if used, _ := st.VotesUsed(ctx, pollID, userID); used != 3 {
			t.Errorf("votes = %d, want 3 -- a refused change must change nothing", used)
		}
		poll, _ := st.StandingPoll(ctx)
		if poll.MaxVotes == 2 {
			t.Error("the budget was changed despite being refused")
		}
	})

	t.Run("trim removes the newest down to the limit", func(t *testing.T) {
		st, pollID, userID := setup(t)
		ctx := context.Background()

		change, err := st.SetMaxVotes(ctx, pollID, 2, PolicyTrim)
		if err != nil {
			t.Fatal(err)
		}
		if change.Trimmed != 1 {
			t.Errorf("Trimmed = %d, want 1", change.Trimmed)
		}
		if used, _ := st.VotesUsed(ctx, pollID, userID); used != 2 {
			t.Errorf("votes = %d, want 2", used)
		}
	})

	t.Run("grandfather keeps the votes but blocks new ones", func(t *testing.T) {
		st, pollID, userID := setup(t)
		ctx := context.Background()

		if _, err := st.SetMaxVotes(ctx, pollID, 2, PolicyGrandfather); err != nil {
			t.Fatal(err)
		}
		if used, _ := st.VotesUsed(ctx, pollID, userID); used != 3 {
			t.Errorf("votes = %d, want 3 -- grandfathering keeps them", used)
		}
		// VotesLeft clamps at zero
		if left, _ := st.VotesLeft(ctx, pollID, userID, 2); left != 0 {
			t.Errorf("VotesLeft = %d, want 0", left)
		}
		extra, _ := st.AddMovie(ctx, "tt0000009", "One more", userID)
		if err := st.Vote(ctx, pollID, userID, extra, 2); !errors.Is(err, ErrNoVotesLeft) {
			t.Errorf("vote while over budget = %v, want ErrNoVotesLeft", err)
		}
	})
}

func TestTheLastAdminCannotBeRemoved(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()

	// The hash is never checked here, any string will do
	admin, err := st.CreateUser(ctx, "admin", "not-a-real-hash", true)
	if err != nil {
		t.Fatal(err)
	}

	if err := st.SetAdmin(ctx, admin.ID, false); !errors.Is(err, ErrLastAdmin) {
		t.Errorf("revoking the last admin = %v, want ErrLastAdmin", err)
	}
	if err := st.DeleteUser(ctx, admin.ID); !errors.Is(err, ErrLastAdmin) {
		t.Errorf("deleting the last admin = %v, want ErrLastAdmin", err)
	}

	// With a second admin in place, both become possible.
	if _, err := st.CreateUser(ctx, "jb", "not-a-real-hash", true); err != nil {
		t.Fatal(err)
	}
	if err := st.SetAdmin(ctx, admin.ID, false); err != nil {
		t.Errorf("revoking with a spare admin present: %v", err)
	}
}
