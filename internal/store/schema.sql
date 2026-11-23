CREATE TABLE IF NOT EXISTS polls (
  id                INTEGER   PRIMARY KEY,
  kind              TEXT      NOT NULL,   -- 'standing' | 'session'
  name              TEXT      NOT NULL,
  max_votes         INTEGER   NOT NULL,   -- N per poll
  join_code         TEXT      UNIQUE,     -- Session polls only
  created_at        INTEGER   NOT NULL,
  closed_at         INTEGER               -- NULL = accepting votes
);

CREATE TABLE IF NOT EXISTS users (
  id                INTEGER   PRIMARY KEY,
  kind              TEXT      NOT NULL DEFAULT 'member',    -- 'member' | 'guest'
  username          TEXT      NOT NULL,
  username_ci       TEXT      NOT NULL,   -- Lowercased, for lookups
  password_hash     TEXT      NOT NULL DEFAULT '',    -- Empty for guests
  is_admin          INTEGER   NOT NULL DEFAULT 0,
  poll_id           INTEGER   REFERENCES polls(id) ON DELETE CASCADE,   -- Guests only
  created_at        INTEGER   NOT NULL
);

-- Members are unique globally; guests only within their own session poll,
-- so a "Jack" on Friday does not block a "Jack" next month.
CREATE UNIQUE INDEX IF NOT EXISTS idx_users_member
  ON users(username_ci) WHERE kind = 'member';
CREATE UNIQUE INDEX IF NOT EXISTS idx_users_guest
  ON users(poll_id, username_ci) WHERE kind = 'guest';

CREATE TABLE IF NOT EXISTS sessions (
  token             TEXT      PRIMARY KEY,    -- 256-bit random, from the cookie
  user_id           INTEGER   NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  csrf              TEXT      NOT NULL, -- Per session
  expires_at        INTEGER   NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_sessions_user ON sessions(user_id);

CREATE TABLE IF NOT EXISTS movies (
  id          INTEGER PRIMARY KEY,
  imdb_id     TEXT    NOT NULL UNIQUE,
  title       TEXT    NOT NULL,
  year        INTEGER,
  runtime_min INTEGER,
  synopsis    TEXT    NOT NULL DEFAULT '',
  poster_file TEXT    NOT NULL DEFAULT '',  -- Local filename, filled in later
  kind        TEXT    NOT NULL DEFAULT 'movie', -- movie | series
  shared      INTEGER NOT NULL DEFAULT 1, -- 0 for a guest's suggestion
  added_by    INTEGER REFERENCES users(id) ON DELETE SET NULL,
  created_at  INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS session_films (
  poll_id     INTEGER NOT NULL REFERENCES polls(id)   ON DELETE CASCADE,
  movie_id    INTEGER NOT NULL REFERENCES movies(id)  ON DELETE CASCADE,
  PRIMARY KEY (poll_id, movie_id)
);

CREATE TABLE IF NOT EXISTS votes (
  poll_id     INTEGER NOT NULL REFERENCES polls(id)   ON DELETE CASCADE,
  user_id     INTEGER NOT NULL REFERENCES users(id)   ON DELETE CASCADE,
  movie_id    INTEGER NOT NULL REFERENCES movies(id)  ON DELETE CASCADE,
  created_at  INTEGER NOT NULL,
  PRIMARY KEY (poll_id, user_id, movie_id)  -- One vote per film, per person, per poll
);
CREATE INDEX IF NOT EXISTS idx_votes_poll_movie ON votes(poll_id, movie_id);

-- reopened_at puts a film back in the running and keeps the record.
-- poll_name is a copy, so it remains even if the session is deleted.
CREATE TABLE IF NOT EXISTS screenings (
  id          INTEGER PRIMARY KEY,
  movie_id    INTEGER NOT NULL REFERENCES movies(id)  ON DELETE CASCADE,
  poll_id     INTEGER          REFERENCES polls(id)   ON DELETE SET NULL,
  poll_name   TEXT    NOT NULL,
  watched_at  INTEGER NOT NULL,
  reopened_at INTEGER
);
CREATE INDEX IF NOT EXISTS idx_screenings_poll ON screenings(poll_id, movie_id);

-- Who voted for a film when it was watched. display_name is a copy, so it
-- survives the user being deleted.
CREATE TABLE IF NOT EXISTS screening_voters (
  id            INTEGER PRIMARY KEY,
  screening_id  INTEGER NOT NULL REFERENCES screenings(id) ON DELETE CASCADE,
  user_id       INTEGER REFERENCES users(id) ON DELETE SET NULL,
  display_name  TEXT    NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_screening_voters ON screening_voters(screening_id, display_name);

CREATE TABLE IF NOT EXISTS ratings (
  movie_id      INTEGER NOT NULL REFERENCES movies(id) ON DELETE CASCADE,
  source        TEXT    NOT NULL, -- imdb | rotten_tomatoes | metacritic | tmdb
  score         REAL    NOT NULL, -- Normalised to 0-100 at write time
  vote_count    INTEGER,          -- Credibility, for later tie-breaks
  fetched_at    INTEGER NOT NULL,
  PRIMARY KEY (movie_id, source)
);

CREATE TABLE IF NOT EXISTS movie_genres (
  movie_id      INTEGER NOT NULL REFERENCES movies(id) ON DELETE CASCADE,
  genre         TEXT    NOT NULL,
  PRIMARY KEY (movie_id, genre)
);

CREATE TABLE IF NOT EXISTS settings (
  key           TEXT PRIMARY KEY,
  value         TEXT NOT NULL
);
