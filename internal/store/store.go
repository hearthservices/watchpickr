// Package store is the only place that knows SQL, by design. It owns all persistence.
package store

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/hearthservices/watchpickr/internal/auth"
	"modernc.org/sqlite"
)

var ErrNotFound = errors.New("not found")

// KindStanding is the one permanent poll. Session polls are disposable.
const (
	KindStanding = "standing"
	KindSession  = "session"
)

var (
	ErrPollClosed = errors.New("that board is closed")
	ErrNotAllowed = errors.New("not allowed")
)

const DefaultMaxVotes = 5

//go:embed schema.sql
var schema string

type Store struct {
	db *sql.DB
}

// Open opens and migrates the database at path. foreign_keys is off by default
// in SQLite, and without it every ON DELETE CASCADE silently does nothing.
// Ask me how I know...
func Open(path string) (*Store, error) {
	dsn := path +
		"?_pragma=busy_timeout(5000)" +
		"&_pragma=journal_mode(WAL)" +
		"&_pragma=foreign_keys(1)" +
		// Take the write lock at BEGIN. A deferred transaction that upgrades later
		// fails with SQLITE_BUSY_SNAPSHOT, which busy_timeout doesn't retry.
		"&_txlock=immediate"

	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping %s: %w", path, err)
	}

	s := &Store{db: db}
	if err := s.migrate(context.Background()); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	for table, cols := range addedColumns {
		if err := s.ensureColumns(ctx, table, cols); err != nil {
			return err
		}
	}
	return s.ensureStandingPoll(ctx)
}

type column struct{ name, decl string }

// addedColumns brings older databases up to date. Anything here must also be in
// schema.sql, which is only applied on a fresh database.
var addedColumns = map[string][]column{
	"movies":  {{"kind", "TEXT NOT NULL DEFAULT 'movie'"}},
	"ratings": {{"fetched_at", "INTEGER NOT NULL DEFAULT 0"}},
}

// ensureColumns adds whichever of the given columns the table is missing.
func (s *Store) ensureColumns(ctx context.Context, table string, want []column) error {
	rows, err := s.db.QueryContext(ctx, `SELECT name FROM pragma_table_info(?)`, table)
	if err != nil {
		return fmt.Errorf("inspect %s: %w", table, err)
	}
	defer rows.Close()

	have := map[string]bool{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return fmt.Errorf("scan column of %s: %w", table, err)
		}
		have[name] = true
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for _, col := range want {
		if have[col.name] {
			continue
		}
		// Identifiers can't be bound, but these come from addedColumns, never a request.
		if _, err := s.db.ExecContext(ctx,
			"ALTER TABLE "+table+" ADD COLUMN "+col.name+" "+col.decl); err != nil {
			return fmt.Errorf("add column %s.%s: %w", table, col.name, err)
		}
	}
	return nil
}

/* ------------------------------------------------------------- polls --- */

// ensureStandingPoll creates the permanent poll on first start.
func (s *Store) ensureStandingPoll(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO polls (kind, name, max_votes, created_at)
SELECT ?, ?, ?, ?
WHERE NOT EXISTS (SELECT 1 FROM polls WHERE kind = ?)`,
		KindStanding, "Movie board", DefaultMaxVotes, time.Now().Unix(), KindStanding)
	if err != nil {
		return fmt.Errorf("create standing poll: %w", err)
	}
	return nil
}

type Poll struct {
	ID       int64
	Kind     string
	Name     string
	MaxVotes int
	JoinCode string
	ClosedAt time.Time // Zero while open
}

func (p Poll) Closed() bool    { return !p.ClosedAt.IsZero() }
func (p Poll) IsSession() bool { return p.Kind == KindSession }

func (s *Store) StandingPoll(ctx context.Context) (*Poll, error) {
	return s.scanPoll(s.db.QueryRowContext(ctx,
		`SELECT `+pollColumns+` FROM polls WHERE kind = ?`, KindStanding))
}

const pollColumns = `id, kind, name, max_votes, COALESCE(join_code, ''), COALESCE(closed_at, 0)`

func (s *Store) scanPoll(row *sql.Row) (*Poll, error) {
	var p Poll
	var closed int64
	if err := row.Scan(&p.ID, &p.Kind, &p.Name, &p.MaxVotes, &p.JoinCode, &closed); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("scan poll: %w", err)
	}
	if closed > 0 {
		p.ClosedAt = time.Unix(closed, 0)
	}
	return &p, nil
}

func (s *Store) PollByID(ctx context.Context, id int64) (*Poll, error) {
	return s.scanPoll(s.db.QueryRowContext(ctx,
		`SELECT `+pollColumns+` FROM polls WHERE id = ?`, id))
}

func (s *Store) PollByJoinCode(ctx context.Context, code string) (*Poll, error) {
	return s.scanPoll(s.db.QueryRowContext(ctx,
		`SELECT `+pollColumns+` FROM polls WHERE join_code = ? AND kind = ?`,
		strings.ToUpper(strings.TrimSpace(code)), KindSession))
}

// PollForUser returns the poll a user votes in: a guest's own session, or the
// standing poll for everyone else. Anything that writes gets its poll from here.
func (s *Store) PollForUser(ctx context.Context, u *User) (*Poll, error) {
	if u.Kind == KindGuest && u.PollID > 0 {
		return s.PollByID(ctx, u.PollID)
	}
	return s.StandingPoll(ctx)
}

func (s *Store) CreateSessionPoll(ctx context.Context, name string, maxVotes int, joinCode string) (*Poll, error) {
	res, err := s.db.ExecContext(ctx, `
INSERT INTO polls (kind, name, max_votes, join_code, created_at)
VALUES (?, ?, ?, ?, ?)`,
		KindSession, name, maxVotes, joinCode, time.Now().Unix())
	if err != nil {
		return nil, fmt.Errorf("create session poll: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return s.PollByID(ctx, id)
}

func (s *Store) SessionPolls(ctx context.Context) ([]Poll, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+pollColumns+` FROM polls WHERE kind = ? ORDER BY created_at DESC`, KindSession)
	if err != nil {
		return nil, fmt.Errorf("list session polls: %w", err)
	}
	defer rows.Close()

	var out []Poll
	for rows.Next() {
		var p Poll
		var closed int64
		if err := rows.Scan(&p.ID, &p.Kind, &p.Name, &p.MaxVotes, &p.JoinCode, &closed); err != nil {
			return nil, fmt.Errorf("scan poll: %w", err)
		}
		if closed > 0 {
			p.ClosedAt = time.Unix(closed, 0)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// ClosePoll stops a session taking votes but keeps it visible.
func (s *Store) ClosePoll(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE polls SET closed_at = ? WHERE id = ? AND kind = ? AND closed_at IS NULL`,
		time.Now().Unix(), id, KindSession)
	if err != nil {
		return fmt.Errorf("close poll: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// DeletePoll removes a session with its guests and votes.
// The record of what was watched remains.
// The standing poll can't be deleted.
func (s *Store) DeletePoll(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM polls WHERE id = ? AND kind = ?`, id, KindSession)
	if err != nil {
		return fmt.Errorf("delete poll: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

/* ------------------------------------------------------------- users --- */

const (
	KindMember = "member"
	KindGuest  = "guest"
)

var (
	ErrDuplicateUser = errors.New("username already taken")
	ErrWrongPassword = errors.New("current password does not match")
)

type User struct {
	ID        int64
	Kind      string
	Username  string
	IsAdmin   bool
	CreatedAt time.Time
	PollID    int64
}

func (s *Store) CreateUser(ctx context.Context, username, passwordHash string, isAdmin bool) (*User, error) {
	now := time.Now()
	res, err := s.db.ExecContext(ctx, `
INSERT INTO users (kind, username, username_ci, password_hash, is_admin, created_at)
VALUES (?, ?, ?, ?, ?, ?)`,
		KindMember, username, ci(username), passwordHash, boolToInt(isAdmin), now.Unix())
	if err != nil {
		if isUniqueViolation(err) {
			return nil, ErrDuplicateUser
		}
		return nil, fmt.Errorf("create user: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("create user: %w", err)
	}
	return &User{ID: id, Kind: KindMember, Username: username, IsAdmin: isAdmin, CreatedAt: now}, nil
}

func (s *Store) CreateGuest(ctx context.Context, pollID int64, name string) (*User, error) {
	now := time.Now()
	res, err := s.db.ExecContext(ctx, `
INSERT INTO users (kind, username, username_ci, is_admin, poll_id, created_at)
VALUES (?, ?, ?, 0, ?, ?)`, KindGuest, name, ci(name), pollID, now.Unix())
	if err != nil {
		if isUniqueViolation(err) {
			return nil, ErrDuplicateUser
		}
		return nil, fmt.Errorf("create guest: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return &User{ID: id, Kind: KindGuest, Username: name, PollID: pollID, CreatedAt: now}, nil
}

// Rename changes a display name. The partial unique indexes keep members unique
// overall and guests unique within their session.
func (s *Store) Rename(ctx context.Context, userID int64, name string) error {
	res, err := s.db.ExecContext(ctx,
		`UPDATE users SET username = ?, username_ci = ? WHERE id = ?`,
		name, ci(name), userID)
	if err != nil {
		if isUniqueViolation(err) {
			return ErrDuplicateUser
		}
		return fmt.Errorf("rename user: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("rename user: %w", err)
	}
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// ChangePassword sets a new password if current is correct. Like Authenticate,
// the hash never leaves the store.
func (s *Store) ChangePassword(ctx context.Context, userID int64, current, next string) error {
	var kind, hash string
	err := s.db.QueryRowContext(ctx,
		`SELECT kind, password_hash FROM users WHERE id = ?`, userID).Scan(&kind, &hash)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("change password: %w", err)
	}
	if kind != KindMember || !auth.CheckPassword(hash, current) {
		return ErrWrongPassword
	}

	nextHash, err := auth.HashPassword(next)
	if err != nil {
		return fmt.Errorf("change password: %w", err)
	}
	if _, err := s.db.ExecContext(ctx,
		`UPDATE users SET password_hash = ? WHERE id = ?`, nextHash, userID); err != nil {
		return fmt.Errorf("change password: %w", err)
	}
	return nil
}

// DeleteOtherSessions logs a user out everywhere except the session they're calling from.
func (s *Store) DeleteOtherSessions(ctx context.Context, userID int64, keep string) error {
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM sessions WHERE user_id = ? AND token <> ?`, userID, keep); err != nil {
		return fmt.Errorf("delete other sessions: %w", err)
	}
	return nil
}

// Authenticate checks a member's password. A missing user and a wrong password
// both return ErrNotFound, and take the same time.
func (s *Store) Authenticate(ctx context.Context, username, password string) (*User, error) {
	var u User
	var hash string
	var admin int
	var created int64

	err := s.db.QueryRowContext(ctx, `
SELECT id, kind, username, password_hash, is_admin, created_at
FROM users
WHERE kind = ? AND username_ci = ?`,
		KindMember, ci(username)).
		Scan(&u.ID, &u.Kind, &u.Username, &hash, &admin, &created)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("authenticate: %w", err)
	}

	// Runs even without a row, so timing doesn't give the username away.
	if !auth.CheckPassword(hash, password) {
		return nil, ErrNotFound
	}

	u.IsAdmin = admin != 0
	u.CreatedAt = time.Unix(created, 0)
	return &u, nil
}

func (s *Store) UserByID(ctx context.Context, id int64) (*User, error) {
	var u User
	var admin int
	var created int64
	err := s.db.QueryRowContext(ctx, `
SELECT id, kind, username, is_admin, created_at, COALESCE(poll_id, 0)
FROM users
WHERE id = ?`, id).
		Scan(&u.ID, &u.Kind, &u.Username, &admin, &created, &u.PollID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("user by id: %w", err)
	}
	u.IsAdmin = admin != 0
	u.CreatedAt = time.Unix(created, 0)
	return &u, nil
}

func (s *Store) CountUsers(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM users
WHERE kind = ?`,
		KindMember).
		Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count users: %w", err)
	}
	return n, nil
}

func (s *Store) AdminExists(ctx context.Context) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `
SELECT COUNT(*)
FROM users
WHERE is_admin = 1`).
		Scan(&n)
	if err != nil {
		return false, fmt.Errorf("admin exists: %w", err)
	}
	return n > 0, nil
}

// EnsureAdmin creates the admin account or resets its password. Other admins
// are left alone.
func (s *Store) EnsureAdmin(ctx context.Context, username, passwordHash string) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO users (kind, username, username_ci, password_hash, is_admin, created_at)
VALUES (?, ?, ?, ?, 1, ?)
-- The unique index on username_ci is partial, so the conflict target has to
-- repeat its predicate for SQLite to match the two up
ON CONFLICT(username_ci) WHERE kind = 'member' DO UPDATE SET
	password_hash 	= excluded.password_hash,
	is_admin		= 1`,
		KindMember, username, ci(username), passwordHash, time.Now().Unix())
	if err != nil {
		return fmt.Errorf("ensure admin: %w", err)
	}
	return nil
}

type Rating struct {
	Source    string
	Score     float64
	VoteCount int
}

func (r Rating) Label() string {
	switch r.Source {
	case "imdb":
		return "IMDb"
	case "rotten_tomatoes":
		return "RT"
	case "metacritic":
		return "Metacritic"
	case "tmdb":
		return "TMDb"
	}
	return r.Source
}

// Metadata is what a lookup found. Empty fields don't overwrite existing data,
// and Title is only set if the person adding the film left it blank.
type Metadata struct {
	Title      string
	Year       int
	RuntimeMin int
	Synopsis   string
	PosterFile string
	Kind       string
	Genres     []string
	Ratings    []Rating
}

func (s *Store) SaveMetadata(ctx context.Context, movieID int64, m Metadata) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `
UPDATE movies SET
		title		= COALESCE(NULLIF(?, ''), title),
		year		= COALESCE(NULLIF(?, 0), year),
		runtime_min	= COALESCE(NULLIF(?, 0), runtime_min),
		synopsis	= COALESCE(NULLIF(?, ''), synopsis),
		poster_file	= COALESCE(NULLIF(?, ''), poster_file),
		kind		= COALESCE(NULLIF(?, ''), kind)
WHERE id = ?`,
		m.Title, m.Year, m.RuntimeMin, m.Synopsis, m.PosterFile, m.Kind, movieID); err != nil {
		return fmt.Errorf("save metadata: %w", err)
	}

	now := time.Now().Unix()
	for _, r := range m.Ratings {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO ratings (movie_id, source, score, vote_count, fetched_at)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(movie_id, source) DO UPDATE SET
	score = excluded.score, vote_count = excluded.vote_count, fetched_at = excluded.fetched_at`,
			movieID, r.Source, r.Score, r.VoteCount, now); err != nil {
			return fmt.Errorf("save rating %s: %w", r.Source, err)
		}
	}
	for _, g := range m.Genres {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO movie_genres (movie_id, genre) VALUES (?, ?) ON CONFLICT DO NOTHING`,
			movieID, g); err != nil {
			return fmt.Errorf("save genre %s: %w", g, err)
		}
	}
	return tx.Commit()
}

// ratingsFor loads the ratings for all the films in one query.
func (s *Store) ratingsFor(ctx context.Context, movies []Movie) error {
	if len(movies) == 0 {
		return nil
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT movie_id, source, score, COALESCE(vote_count, 0) FROM ratings ORDER BY source`)
	if err != nil {
		return fmt.Errorf("load ratings: %w", err)
	}
	defer rows.Close()

	byMovie := map[int64][]Rating{}
	for rows.Next() {
		var id int64
		var r Rating
		if err := rows.Scan(&id, &r.Source, &r.Score, &r.VoteCount); err != nil {
			return fmt.Errorf("scan rating: %w", err)
		}
		byMovie[id] = append(byMovie[id], r)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for i := range movies {
		movies[i].Ratings = byMovie[movies[i].ID]
	}
	return nil
}

/* ------------------------------------------------------------- sessions --- */

func (s *Store) CreateSession(ctx context.Context, userID int64, token, csrf string, expires time.Time) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO sessions (token, user_id, csrf, expires_at)
VALUES (?, ?, ?, ?)`,
		token, userID, csrf, expires.Unix())
	if err != nil {
		return fmt.Errorf("create session: %w", err)
	}
	return nil
}

// Session resolves a cookie token to its user, ignoring expired rows.
func (s *Store) Session(ctx context.Context, token string) (*User, string, error) {
	var u User
	var admin int
	var created int64
	var csrf string

	err := s.db.QueryRowContext(ctx, `
SELECT u.id, u.kind, u.username, u.is_admin, u.created_at, s.csrf, COALESCE(u.poll_id, 0)
FROM sessions s JOIN users u ON u.id = s.user_id
WHERE s.token = ? AND s.expires_at > ?`,
		token, time.Now().Unix()).
		Scan(&u.ID, &u.Kind, &u.Username, &admin, &created, &csrf, &u.PollID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, "", ErrNotFound
	}
	if err != nil {
		return nil, "", fmt.Errorf("session: %w", err)
	}
	u.IsAdmin = admin != 0
	u.CreatedAt = time.Unix(created, 0)
	return &u, csrf, nil
}

func (s *Store) DeleteSession(ctx context.Context, token string) error {
	_, err := s.db.ExecContext(ctx, `
DELETE FROM sessions
WHERE token = ?`,
		token)
	return err
}

func (s *Store) DeleteExpiredSessions(ctx context.Context) (int64, error) {
	res, err := s.db.ExecContext(ctx, `
DELETE FROM sessions
WHERE expires_at <= ?`,
		time.Now().Unix())
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

/* -------------------------------------------------------------- movies --- */

var (
	ErrDuplicateMovie = errors.New("that film is already on the board")
	ErrNoVotesLeft    = errors.New("no votes left")
	ErrAlreadyVoted   = errors.New("already voted for this film")
	ErrMovieWatched   = errors.New("that film has already been watched")
)

type Movie struct {
	ID        int64
	IMDbID    string
	Title     string
	Year      int
	Synopsis  string
	Kind      string
	CreatedAt time.Time

	// AddedByID is for permission checks, AddedBy is for display.
	AddedByID int64
	AddedBy   string

	Votes     int
	VotedByMe bool
	WatchedAt time.Time // Zero when still in the running
	Rank      int       // 1-based among films in the running, 0 when watched

	// WatchedElsewhere is the latest screening on another board (nil if none).
	WatchedElsewhere *Screening

	RuntimeMin int
	PosterFile string
	Ratings    []Rating
	Genres     []string
}

func (m Movie) Watched() bool { return !m.WatchedAt.IsZero() }

func (m Movie) IMDbURL() string { return "https://www.imdb.com/title/" + m.IMDbID + "/" }

// AddMovie adds a film to the standing board. If a session already has it, it
// gets promoted there instead of re-imported.
func (s *Store) AddMovie(ctx context.Context, imdbID, title string, addedBy int64) (int64, error) {
	var id int64
	err := s.db.QueryRowContext(ctx, `
INSERT INTO movies (imdb_id, title, added_by, created_at) VALUES (?, ?, ?, ?)
ON CONFLICT(imdb_id) DO UPDATE SET
	shared = 1, added_by = excluded.added_by, created_at = excluded.created_at
WHERE shared = 0
RETURNING id`,
		imdbID, title, addedBy, time.Now().Unix()).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		// Already shared, so nothing to update.
		return 0, ErrDuplicateMovie
	}
	if err != nil {
		return 0, fmt.Errorf("add movie: %w", err)
	}
	return id, nil
}

// AddSessionMovie adds a film to one session only. But another session
// adding the same film just gets a link to the existing one.
func (s *Store) AddSessionMovie(ctx context.Context, pollID int64, imdbID, title string, addedBy int64) (int64, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, `
INSERT INTO movies (imdb_id, title, shared, added_by, created_at) VALUES (?, ?, 0, ?, ?)
ON CONFLICT(imdb_id) DO NOTHING`,
		imdbID, title, addedBy, time.Now().Unix()); err != nil {
		return 0, fmt.Errorf("add session movie: %w", err)
	}

	var id int64
	err = tx.QueryRowContext(ctx, `
INSERT INTO session_films (poll_id, movie_id)
SELECT ?, id FROM movies WHERE imdb_id = ? AND shared = 0
RETURNING movie_id`,
		pollID, imdbID).Scan(&id)
	switch {
	case errors.Is(err, sql.ErrNoRows), isUniqueViolation(err):
		return 0, ErrDuplicateMovie
	case err != nil:
		return 0, fmt.Errorf("link session movie: %w", err)
	}
	return id, tx.Commit()
}

// onBoard is the SQL condition for a film being on a poll's board.
const onBoard = `(m.shared = 1 OR m.id IN (SELECT movie_id FROM session_films WHERE poll_id = ?))`

// Movies returns the films on a poll's board in display order:
// unwatched first, then by votes, IMDb score, year, and when it was added.
func (s *Store) Movies(ctx context.Context, pollID, viewerID int64) ([]Movie, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT m.id, m.imdb_id, m.title, COALESCE(m.year, 0), m.synopsis, m.created_at,
	COALESCE(m.runtime_min, 0), m.poster_file, m.kind,
	COALESCE(m.added_by, 0), COALESCE(u.username, ''),
	(SELECT COUNT(*) FROM votes v
		WHERE v.poll_id = ? AND v.movie_id = m.id) AS votes,
	EXISTS(SELECT 1 FROM votes v2
		WHERE v2.poll_id = ? AND v2.movie_id = m.id AND v2.user_id = ?) AS mine,
	COALESCE((SELECT sc.watched_at FROM screenings sc
		WHERE sc.movie_id = m.id AND sc.poll_id = ?
		AND sc.reopened_at IS NULL
		ORDER BY sc.watched_at DESC LIMIT 1), 0) AS watched_at,
	(SELECT r.score FROM ratings r
		WHERE r.movie_id = m.id AND r.source = 'imdb') AS imdb_score
FROM movies m
LEFT JOIN users u ON u.id = m.added_by
WHERE `+onBoard+`
ORDER BY (watched_at > 0) ASC,
	votes DESC,
	imdb_score DESC,
	COALESCE(m.year, 0) DESC,
	m.created_at ASC`,
		pollID, pollID, viewerID, pollID, pollID)
	if err != nil {
		return nil, fmt.Errorf("list movies: %w", err)
	}
	defer rows.Close()

	var out []Movie
	rank := 0
	for rows.Next() {
		var m Movie
		var created, watched int64
		var mine int
		var imdbScore sql.NullFloat64
		if err := rows.Scan(&m.ID, &m.IMDbID, &m.Title, &m.Year, &m.Synopsis, &created,
			&m.RuntimeMin, &m.PosterFile, &m.Kind, &m.AddedByID, &m.AddedBy, &m.Votes, &mine, &watched, &imdbScore); err != nil {
			return nil, fmt.Errorf("scan movie: %w", err)
		}
		m.CreatedAt = time.Unix(created, 0)
		m.VotedByMe = mine != 0
		if watched > 0 {
			m.WatchedAt = time.Unix(watched, 0)
		} else {
			rank++
			m.Rank = rank
		}
		out = append(out, m)
	}
	if err := s.ratingsFor(ctx, out); err != nil {
		return nil, err
	}
	if err := s.genresFor(ctx, out); err != nil {
		return nil, err
	}
	if err := s.watchedElsewhere(ctx, pollID, out); err != nil {
		return nil, err
	}
	return out, rows.Err()
}

func (s *Store) MovieByID(ctx context.Context, pollID, viewerID, id int64) (*Movie, error) {
	movies, err := s.Movies(ctx, pollID, viewerID)
	if err != nil {
		return nil, err
	}
	for i := range movies {
		if movies[i].ID == id {
			return &movies[i], nil
		}
	}
	return nil, ErrNotFound
}

func (s *Store) DeleteMovie(ctx context.Context, id int64) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM movies WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete movie: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

/* ---------------------------------------------------------------- votes --- */

func (s *Store) VotesUsed(ctx context.Context, pollID, userID int64) (int, error) {
	var n int
	// No join needed, watching a film deletes its votes.
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM votes WHERE poll_id = ? AND user_id = ?`,
		pollID, userID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("votes used: %w", err)
	}
	return n, nil
}

func (s *Store) VotesLeft(ctx context.Context, pollID, userID int64, max int) (int, error) {
	used, err := s.VotesUsed(ctx, pollID, userID)
	if err != nil {
		return 0, err
	}
	// Clamped, lowering N can leave someone over budget.
	if left := max - used; left > 0 {
		return left, nil
	}
	return 0, nil
}

// Vote records one vote. The single statement below checks every rule (the film
// is on the board, isn't watched, the voter has votes left), so nothing can slip in
// between a check and the insert.
// Is it overkill for this application? Probably.
// Am I still kinda proud over it? Definitely.
func (s *Store) Vote(ctx context.Context, pollID, userID, movieID int64, max int) error {
	res, err := s.db.ExecContext(ctx, `
INSERT INTO votes (poll_id, user_id, movie_id, created_at)
SELECT ?, ?, ?, ?
WHERE EXISTS (
	SELECT 1 FROM movies m
	WHERE m.id = ? AND `+onBoard+`
	AND NOT EXISTS (SELECT 1 FROM screenings sc
					WHERE sc.movie_id = m.id AND sc.poll_id = ?
					AND sc.reopened_at IS NULL))
	AND (SELECT COUNT(*) FROM votes v
		WHERE v.poll_id = ? AND v.user_id = ?) < ?
AND EXISTS (SELECT 1 FROM polls WHERE id = ? AND closed_at IS NULL)`,
		pollID, userID, movieID, time.Now().Unix(),
		movieID, pollID, pollID,
		pollID, userID, max, pollID)
	if err != nil {
		if isUniqueViolation(err) {
			return ErrAlreadyVoted
		}
		return fmt.Errorf("vote: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		// Only a refusal pays for a second query to find out why.
		return s.voteRefusal(ctx, pollID, userID, movieID, max)
	}
	return nil
}

func (s *Store) voteRefusal(ctx context.Context, pollID, userID, movieID int64, max int) error {
	var exists, watched, used, open int
	err := s.db.QueryRowContext(ctx, `
SELECT  (SELECT COUNT(*) FROM movies WHERE id = ?),
		(SELECT COUNT(*) FROM screenings
			WHERE movie_id = ? AND poll_id = ? AND reopened_at IS NULL),
		(SELECT COUNT(*) FROM votes WHERE poll_id = ? AND user_id = ?),
		(SELECT COUNT(*) FROM polls WHERE id = ? AND closed_at IS NULL)`,
		movieID, movieID, pollID, pollID, userID, pollID).Scan(&exists, &watched, &used, &open)
	if err != nil {
		return fmt.Errorf("diagnose vote refusal: %w", err)
	}
	switch {
	case exists == 0:
		return ErrNotFound
	case open == 0:
		return ErrPollClosed
	case watched > 0:
		return ErrMovieWatched
	case used >= max:
		return ErrNoVotesLeft
	}
	// The board changed in between, rare and harmless
	return ErrNotFound
}

func (s *Store) Unvote(ctx context.Context, pollID, userID, movieID int64) error {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM votes WHERE poll_id = ? AND user_id = ? AND movie_id = ?`,
		pollID, userID, movieID)
	if err != nil {
		return fmt.Errorf("unvote: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

/* ----------------------------------------------------------- screenings --- */

// MarkWatched records a screening and gives the voters their votes back.
// The snapshot info has to be taken before the votes are deleted.
func (s *Store) MarkWatched(ctx context.Context, pollID, movieID int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // No-op once Commit has run

	now := time.Now().Unix()
	res, err := tx.ExecContext(ctx, `
INSERT INTO screenings (movie_id, poll_id, poll_name, watched_at)
SELECT ?, p.id, p.name, ?
FROM polls p
WHERE p.id = ?
AND EXISTS (SELECT 1 FROM movies WHERE id = ?)
AND NOT EXISTS (SELECT 1 FROM screenings
				WHERE movie_id = ? AND poll_id = ? AND reopened_at IS NULL)`,
		movieID, now, pollID, movieID, movieID, pollID)
	if err != nil {
		return fmt.Errorf("record screening: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrMovieWatched // Or the film is gone
	}
	screeningID, err := res.LastInsertId()
	if err != nil {
		return fmt.Errorf("record screening: %w", err)
	}

	if _, err := tx.ExecContext(ctx, `
INSERT INTO screening_voters (screening_id, user_id, display_name)
SELECT ?, u.id, u.username
FROM votes v JOIN users u ON u.id = v.user_id
WHERE v.poll_id = ? AND v.movie_id = ?`,
		screeningID, pollID, movieID); err != nil {
		return fmt.Errorf("snapshot voters: %w", err)
	}

	if _, err := tx.ExecContext(ctx,
		`DELETE FROM votes WHERE poll_id = ? AND movie_id = ?`, pollID, movieID); err != nil {
		return fmt.Errorf("refund votes: %w", err)
	}
	return tx.Commit()
}

// Reopen puts a watched film back in the running. Old votes aren't restored,
// anyone who wants it can now vote for it again.
func (s *Store) Reopen(ctx context.Context, pollID, movieID int64) error {
	res, err := s.db.ExecContext(ctx, `
UPDATE screenings SET reopened_at = ?
WHERE movie_id = ? AND poll_id = ? AND reopened_at IS NULL`,
		time.Now().Unix(), movieID, pollID)
	if err != nil {
		return fmt.Errorf("reopen: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Voters returns who backed the film in its latest screening.
func (s *Store) Voters(ctx context.Context, pollID, movieID int64) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT sv.display_name
FROM screening_voters sv
JOIN screenings sc ON sc.id = sv.screening_id
WHERE sc.movie_id = ? AND sc.poll_id = ?
AND sc.watched_at = (SELECT MAX(watched_at) FROM screenings
						WHERE movie_id = ? AND poll_id = ?)
ORDER BY sv.display_name`,
		movieID, pollID, movieID, pollID)
	if err != nil {
		return nil, fmt.Errorf("voters: %w", err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out = append(out, name)
	}
	return out, rows.Err()
}

// Screening is where and when a film was watched
type Screening struct {
	PollName  string
	WatchedAt time.Time
}

// watchedElsewhere finds the latest screening of each film on other boards,
// including deleted ones.
func (s *Store) watchedElsewhere(ctx context.Context, pollID int64, movies []Movie) error {
	if len(movies) == 0 {
		return nil
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT movie_id, poll_name, watched_at FROM screenings
WHERE reopened_at IS NULL AND (poll_id IS NULL OR poll_id <> ?)
ORDER BY watched_at`, pollID)
	if err != nil {
		return fmt.Errorf("load screenings: %w", err)
	}
	defer rows.Close()

	// Oldest first, so each film ends up with its latest screening.
	latest := map[int64]*Screening{}
	for rows.Next() {
		var id, watched int64
		var sc Screening
		if err := rows.Scan(&id, &sc.PollName, &watched); err != nil {
			return fmt.Errorf("scan screening: %w", err)
		}
		sc.WatchedAt = time.Unix(watched, 0)
		latest[id] = &sc
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for i := range movies {
		movies[i].WatchedElsewhere = latest[movies[i].ID]
	}
	return nil
}

/* -------------------------------------------------------------- helpers --- */

func ci(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// SQLite extended result codes. Safer to match on than error text.
const (
	sqliteConstraintPrimaryKey = 1555
	sqliteConstraintUnique     = 2067
)

func isUniqueViolation(err error) bool {
	var se *sqlite.Error
	if !errors.As(err, &se) {
		return false
	}
	switch se.Code() {
	case sqliteConstraintUnique, sqliteConstraintPrimaryKey:
		return true
	}
	return false
}

// ratingPreference is the order scores are tried in when one number is needed.
// TMDb is last since it's only a fallback.
var ratingPreference = []string{"imdb", "metacritic", "rotten_tomatoes", "tmdb"}

// Score returns the rating used for sorting and filtering, and false if there
// is none.
func (m Movie) Score() (float64, bool) {
	for _, want := range ratingPreference {
		for _, r := range m.Ratings {
			if r.Source == want {
				return r.Score, true
			}
		}
	}
	return 0, false
}

// ScoreLabel formats Score for templates, which can't take two return values.
func (m Movie) ScoreLabel() string {
	score, ok := m.Score()
	if !ok {
		return ""
	}
	return strconv.FormatFloat(score, 'f', 0, 64)
}

// genresFor loads the genres for all the films in one query, like ratingsFor.
func (s *Store) genresFor(ctx context.Context, movies []Movie) error {
	if len(movies) == 0 {
		return nil
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT movie_id, genre FROM movie_genres ORDER BY genre`)
	if err != nil {
		return fmt.Errorf("load genres: %w", err)
	}
	defer rows.Close()

	byMovie := map[int64][]string{}
	for rows.Next() {
		var id int64
		var genre string
		if err := rows.Scan(&id, &genre); err != nil {
			return fmt.Errorf("scan genre: %w", err)
		}
		byMovie[id] = append(byMovie[id], genre)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for i := range movies {
		movies[i].Genres = byMovie[movies[i].ID]
	}
	return nil
}

/* -------------------------------------------------------------- settings --- */

// Settings the admin can change at runtime.

const SettingAllowSeries = "allow_series"

func (s *Store) Setting(ctx context.Context, key, fallback string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return fallback, nil
	}
	if err != nil {
		return "", fmt.Errorf("setting %s: %w", key, err)
	}
	return v, nil
}

func (s *Store) SetSetting(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO settings (key, value) VALUES (?, ?)
ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	if err != nil {
		return fmt.Errorf("set %s: %w", key, err)
	}
	return nil
}

// BoolSetting reads a flag, falling back on anything unparseable.
func (s *Store) BoolSetting(ctx context.Context, key string, fallback bool) (bool, error) {
	raw, err := s.Setting(ctx, key, strconv.FormatBool(fallback))
	if err != nil {
		return fallback, err
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		return fallback, nil
	}
	return v, nil
}

const (
	PolicyRefuse      = "refuse"
	PolicyTrim        = "trim"
	PolicyGrandfather = "grandfather"
)

var (
	ErrOverBudget = errors.New("somebody would be left over budget")
	ErrLastAdmin  = errors.New("that is the last administrator")
)

// BudgetChange reports what SetMaxVotes did, for the admin page.
type BudgetChange struct {
	OverBudget []string // Who would be over budget
	Trimmed    int      // Votes removed by PolicyTrim
}

// SetMaxVotes changes a poll's vote budget. If that leaves people over the
// limit, policy decides what happens to their extra votes.
func (s *Store) SetMaxVotes(ctx context.Context, pollID int64, max int, policy string) (*BudgetChange, error) {
	if max < 1 {
		return nil, fmt.Errorf("vote budget must be at least 1, got %d", max)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	over, err := overBudget(ctx, tx, pollID, max)
	if err != nil {
		return nil, err
	}
	change := &BudgetChange{OverBudget: over}

	switch policy {
	case PolicyRefuse:
		if len(over) > 0 {
			return change, ErrOverBudget
		}
	case PolicyTrim:
		if len(over) > 0 {
			n, err := trimVotes(ctx, tx, pollID, max)
			if err != nil {
				return nil, err
			}
			change.Trimmed = n
		}
	case PolicyGrandfather:
		// They keep what they have, but can't vote again until they're under the limit.
	default:
		return nil, fmt.Errorf("unknown budget policy %q", policy)
	}

	if _, err := tx.ExecContext(ctx,
		`UPDATE polls SET max_votes = ? WHERE id = ?`, max, pollID); err != nil {
		return nil, fmt.Errorf("set max votes: %w", err)
	}
	return change, tx.Commit()
}

func overBudget(ctx context.Context, tx *sql.Tx, pollID int64, max int) ([]string, error) {
	rows, err := tx.QueryContext(ctx, `
SELECT u.username
FROM votes v JOIN users u ON u.id = v.user_id
WHERE v.poll_id = ?
GROUP BY v.user_id
HAVING COUNT(*) > ?
ORDER BY u.username`, pollID, max)
	if err != nil {
		return nil, fmt.Errorf("find over-budget voters: %w", err)
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, err
		}
		out = append(out, name)
	}
	return out, rows.Err()
}

// trimVotes removes each over-budget voter's newest votes, assuming the oldest
// ones matter most to them.
func trimVotes(ctx context.Context, tx *sql.Tx, pollID int64, max int) (int, error) {
	res, err := tx.ExecContext(ctx, `
DELETE FROM votes
WHERE rowid IN (
	SELECT rowid FROM (
		SELECT rowid,
				ROW_NUMBER() OVER (
				PARTITION By user_id
				ORDER BY created_at DESC, rowid DESC) as rn
		FROM votes
		WHERE poll_id = ?)
	WHERE rn > ?)`, pollID, max)
	if err != nil {
		return 0, fmt.Errorf("trim votes: %w", err)
	}
	n, err := res.RowsAffected()
	return int(n), err
}

/* -------------------------------------------------------------- admin-page: users --- */

type UserStat struct {
	User
	VotesUsed int
	Suggested int
}

func (s *Store) UserStats(ctx context.Context, pollID int64) ([]UserStat, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT u.id, u.kind, u.username, u.is_admin, u.created_at,
	(SELECT COUNT(*) FROM votes v WHERE v.user_id = u.id AND v.poll_id = ?),
	(SELECT COUNT(*) FROM movies m WHERE m.added_by = u.id)
FROM users u
WHERE u.kind = ?
ORDER BY u.is_admin DESC, u.username_ci ASC`, pollID, KindMember)
	if err != nil {
		return nil, fmt.Errorf("user stats: %w", err)
	}
	defer rows.Close()

	var out []UserStat
	for rows.Next() {
		var st UserStat
		var admin int
		var created int64
		if err := rows.Scan(&st.ID, &st.Kind, &st.Username, &admin, &created,
			&st.VotesUsed, &st.Suggested); err != nil {
			return nil, fmt.Errorf("scan user stat: %w", err)
		}
		st.IsAdmin = admin != 0
		st.CreatedAt = time.Unix(created, 0)
		out = append(out, st)
	}
	return out, rows.Err()
}

// SetAdmin grants or revokes admin, but never removes the last one.
func (s *Store) SetAdmin(ctx context.Context, id int64, isAdmin bool) error {
	if isAdmin {
		res, err := s.db.ExecContext(ctx,
			`UPDATE users SET is_admin = 1 WHERE id = ? AND kind = ?`, id, KindMember)
		if err != nil {
			return fmt.Errorf("grant admin: %w", err)
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		return nil
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var others int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM users WHERE is_admin = 1 AND id <> ?`, id).Scan(&others); err != nil {
		return fmt.Errorf("count admins: %w", err)
	}
	if others == 0 {
		return ErrLastAdmin
	}
	res, err := tx.ExecContext(ctx, `UPDATE users SET is_admin = 0 WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("revoke admin: %w", err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return tx.Commit()
}

// DeleteUser removes a member and their votes. Their suggestions stay, without
// a name.
func (s *Store) DeleteUser(ctx context.Context, id int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var isAdmin, others int
	if err := tx.QueryRowContext(ctx,
		`SELECT is_admin FROM users WHERE id = ?`, id).Scan(&isAdmin); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("look up user: %w", err)
	}
	if isAdmin != 0 {
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM users WHERE is_admin = 1 AND id <> ?`, id).Scan(&others); err != nil {
			return fmt.Errorf("count admins: %w", err)
		}
		if others == 0 {
			return ErrLastAdmin
		}
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, id); err != nil {
		return fmt.Errorf("delete user: %w", err)
	}
	return tx.Commit()
}
