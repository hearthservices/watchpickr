package web

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hearthservices/watchpickr/internal/config"
	"github.com/hearthservices/watchpickr/internal/metadata"
	"github.com/hearthservices/watchpickr/internal/poster"
	"github.com/hearthservices/watchpickr/internal/store"
)

type testLog struct{ t *testing.T }

func (w testLog) Write(p []byte) (int, error) { w.t.Logf("%s", p); return len(p), nil }

// newTestServer builds a Server on a temporary database, opened the same way
// as in production.
func newTestServer(t *testing.T) *Server {
	t.Helper()

	st, err := store.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	cfg := config.Config{
		SiteName:   "WatchPickr",
		SessionTTL: time.Hour,
	}
	log := slog.New(slog.NewTextHandler(testLog{t}, nil))

	meta := metadata.New("", "", nil)
	posters, err := poster.NewCache(t.TempDir(), nil)
	if err != nil {
		t.Fatalf("poster cache: %v", err)
	}

	srv, err := New(cfg, st, meta, posters, log)
	if err != nil {
		t.Fatalf("new server: %v", err)
	}
	return srv
}

// get sends a request through the whole server, routing and middleware included.
func get(t *testing.T, srv *Server, target string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, target, nil))
	return rec
}

func TestRoutes(t *testing.T) {
	srv := newTestServer(t)

	tests := []struct {
		name       string
		target     string
		wantStatus int
		wantBody   string // Substring, empty means don't check
	}{
		{"health check", "/healthz", http.StatusOK, `"status":"ok"`},
		//{"board renders the standing poll", "/", http.StatusOK, "Movie board"},
		{"board redirects when anonymous", "/", http.StatusSeeOther, ""},
		{"stylesheet is served", "/static/style.css", http.StatusOK, ""},
		{"unknown path", "/nope", http.StatusNotFound, ""},
		{"root does not swallow subpaths", "/deeper/still", http.StatusNotFound, ""},
		{"board fragment", "/board", http.StatusSeeOther, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := get(t, srv, tt.target)

			if rec.Code != tt.wantStatus {
				t.Errorf("GET %s status = %d, want %d\n--- body ---\n%s",
					tt.target, rec.Code, tt.wantStatus, rec.Body.String())
			}
			if tt.wantBody != "" && !strings.Contains(rec.Body.String(), tt.wantBody) {
				t.Errorf("Get %s body missing %q\n--- body ---\n%s",
					tt.target, tt.wantBody, rec.Body.String())
			}
		})
	}
}

func TestSecurityHeaders(t *testing.T) {
	srv := newTestServer(t)
	rec := get(t, srv, "/")

	if got := rec.Header().Get("Content-Security-Policy"); !strings.Contains(got, "default-src 'self'") {
		t.Errorf("Content-Security-Policy = %q, want it to restrict default-src", got)
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want %q", got, "nosniff")
	}
}

func TestAssetCaching(t *testing.T) {
	srv := newTestServer(t)

	t.Run("versioned URLs cache hard", func(t *testing.T) {
		rec := get(t, srv, "/static/style.css?v="+srv.assetVersion)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
		}
		if got := rec.Header().Get("Content-Type"); !strings.Contains(got, "text/css") {
			t.Errorf("Content-Type = %q, want text/css", got)
		}
		if got := rec.Header().Get("Cache-Control"); !strings.Contains(got, "immutable") {
			t.Errorf("Cache-Control = %q, want immutable for a versioned URL", got)
		}
	})

	t.Run("unversioned URLs revalidate", func(t *testing.T) {
		rec := get(t, srv, "/static/style.css")

		if got := rec.Header().Get("Cache-Control"); !strings.Contains(got, "must-revalidate") {
			t.Errorf("Cache-Control = %q, want must-revalidate for an unversioned URL", got)
		}
	})
}

// registerAndLogin signs up through the real HTTP flow, CSRF included, and
// returns the cookies.
func registerAndLogin(t *testing.T, srv *Server, username, password string) []*http.Cookie {
	t.Helper()

	form := httptest.NewRecorder()
	srv.ServeHTTP(form, httptest.NewRequest(http.MethodGet, "/register", nil))

	var csrf string
	for _, c := range form.Result().Cookies() {
		if c.Name == anonCSRFCookie {
			csrf = c.Value
		}
	}
	if csrf == "" {
		t.Fatal("no anonymous CSRF cookie was issued")
	}

	body := url.Values{"username": {username}, "password": {password}, "csrf": {csrf}}
	req := httptest.NewRequest(http.MethodPost, "/register", strings.NewReader(body.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for _, c := range form.Result().Cookies() {
		req.AddCookie(c)
	}

	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("register status = %d, want %d\n%s", rec.Code, http.StatusSeeOther, rec.Body.String())
	}
	return rec.Result().Cookies()
}

func TestAuth(t *testing.T) {
	srv := newTestServer(t)
	cookies := registerAndLogin(t, srv, "jb", "correct horse battery")

	t.Run("board renders once logged in", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		for _, c := range cookies {
			req.AddCookie(c)
		}
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "Movie board") {
			t.Errorf("board body = %s", rec.Body.String())
		}
	})

	t.Run("logout without a CSRF token is refused", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/logout", nil)
		for _, c := range cookies {
			req.AddCookie(c)
		}
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)

		if rec.Code != http.StatusForbidden {
			t.Errorf("status %d, want %d", rec.Code, http.StatusForbidden)
		}
	})

	t.Run("HEAD on a gated page is not treated as a write", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodHead, "/", nil)
		for _, c := range cookies {
			req.AddCookie(c)
		}
		rec := httptest.NewRecorder()
		srv.ServeHTTP(rec, req)

		if rec.Code != http.StatusOK {
			t.Errorf("status = %d, want 200 -- HEAD is falling into the CSRF branch", rec.Code)
		}
	})
}

// sessionCSRF reads the token from the store instead of a rendered form.
func sessionCSRF(t *testing.T, srv *Server, cookies []*http.Cookie) string {
	t.Helper()
	for _, c := range cookies {
		if c.Name == sessionCookie {
			_, csrf, err := srv.store.Session(context.Background(), c.Value)
			if err != nil {
				t.Fatalf("session: %v", err)
			}
			return csrf
		}
	}
	t.Fatal("no session cookie")
	return ""
}

func TestBoardRendersWithFilmsForAPlainMember(t *testing.T) {
	srv := newTestServer(t)
	cookies := registerAndLogin(t, srv, "jb", "correct horse battery")
	csrf := sessionCSRF(t, srv, cookies)

	form := url.Values{
		"csrf":  {csrf},
		"link":  {"https://www.imdb.com/title/tt0111161/"},
		"title": {"The Shawshank Redemption"},
	}
	req := httptest.NewRequest(http.MethodPost, "/movies", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("add film status = %d, want 303\n%s", rec.Code, rec.Body.String())
	}

	// Render as a plain member. For admins the delete guard's `or` short-circuits,
	// so half the card would never be evaluated.
	req = httptest.NewRequest(http.MethodGet, "/", nil)
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("board status = %d, want 200\n%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Shawshank") {
		t.Errorf("board rendered without the film:\n%s", rec.Body.String())
	}
}

// send makes a request with the given cookies. A form makes it a POST.
func send(t *testing.T, srv *Server, target string, form url.Values, cookies []*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, target, nil)
	if form != nil {
		req = httptest.NewRequest(http.MethodPost, target, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

// joinAsGuest goes through the join form and returns the guest's cookies.
func joinAsGuest(t *testing.T, srv *Server, code, name string) []*http.Cookie {
	t.Helper()
	page := send(t, srv, "/join/"+code, nil, nil)

	var csrf string
	for _, c := range page.Result().Cookies() {
		if c.Name == anonCSRFCookie {
			csrf = c.Value
		}
	}
	rec := send(t, srv, "/join/"+code, url.Values{"name": {name}, "csrf": {csrf}}, page.Result().Cookies())
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("join status = %d, want %d\n%s", rec.Code, http.StatusSeeOther, rec.Body.String())
	}
	return rec.Result().Cookies()
}

// TestGuestSeesTheirOwnSession checks that a guest is shown their own board.
// The store keeps the boards apart, so this checks what the board asks it for.
func TestGuestSeesTheirOwnSession(t *testing.T) {
	srv := newTestServer(t)
	ctx := context.Background()

	session, err := srv.store.CreateSessionPoll(ctx, "Friday", 2, "ABC234")
	if err != nil {
		t.Fatal(err)
	}
	guest := joinAsGuest(t, srv, session.JoinCode, "jc")

	add := url.Values{
		"csrf":  {sessionCSRF(t, srv, guest)},
		"link":  {"https://www.imdb.com/title/tt0245429/"},
		"title": {"Spirited Away"},
	}
	if rec := send(t, srv, "/movies", add, guest); rec.Code != http.StatusSeeOther {
		t.Fatalf("suggest status = %d, want %d", rec.Code, http.StatusSeeOther)
	}
	films, err := srv.store.Movies(ctx, session.ID, 0)
	if err != nil || len(films) != 1 {
		t.Fatalf("session films = %v, %v; want the suggestion", films, err)
	}
	vote := url.Values{"csrf": {sessionCSRF(t, srv, guest)}}
	if rec := send(t, srv, fmt.Sprintf("/movies/%d/vote", films[0].ID), vote, guest); rec.Code != http.StatusSeeOther {
		t.Fatalf("vote status = %d, want %d", rec.Code, http.StatusSeeOther)
	}

	body := send(t, srv, "/", nil, guest).Body.String()
	for _, want := range []string{"Spirited Away", "1 vote</span>", "<strong>1</strong> of 2 votes left"} {
		if !strings.Contains(body, want) {
			t.Errorf("guest board is missing %q", want)
		}
	}

	member := registerAndLogin(t, srv, "jb", "correct horse battery")
	if body := send(t, srv, "/", nil, member).Body.String(); strings.Contains(body, "Spirited Away") {
		t.Error("a guest's suggestion showed up on the standing board")
	}
}
