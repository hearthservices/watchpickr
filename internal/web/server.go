// Package web wires the HTTP layer: routing, templates, and static assets.
package web

import (
	"bytes"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/hearthservices/watchpickr/internal/browse"
	"github.com/hearthservices/watchpickr/internal/config"
	"github.com/hearthservices/watchpickr/internal/metadata"
	"github.com/hearthservices/watchpickr/internal/poster"
	"github.com/hearthservices/watchpickr/internal/store"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static
var staticFS embed.FS

type Server struct {
	cfg     config.Config
	store   *store.Store
	log     *slog.Logger
	tmpl    map[string]*template.Template
	mux     *http.ServeMux
	logins  *throttle
	hub     *hub
	meta    *metadata.Service
	posters *poster.Cache

	// bg outlives requests, for lookups that finish after the response is sent.
	bg     context.Context
	stopBG context.CancelFunc
	bgWork sync.WaitGroup

	// assetVersion is a hash of the CSS and JS, added to their URLs for caching.
	assetVersion string
}

func New(cfg config.Config, st *store.Store, meta *metadata.Service, posters *poster.Cache, log *slog.Logger) (*Server, error) {
	tmpl, err := parseTemplates()
	if err != nil {
		return nil, err
	}
	version, err := hashAssets()
	if err != nil {
		return nil, err
	}
	bg, stopBG := context.WithCancel(context.Background())
	s := &Server{
		cfg:          cfg,
		store:        st,
		meta:         meta,
		posters:      posters,
		log:          log,
		tmpl:         tmpl,
		logins:       newThrottle(10, 15*time.Minute),
		hub:          newHub(),
		bg:           bg,
		stopBG:       stopBG,
		assetVersion: version,
	}
	s.routes()
	go s.hub.run()
	return s, nil
}

// Close ends the event streams. Call it before http.Server.Shutdown, which
// would otherwise wait for them until it times out.
func (s *Server) Close() {
	s.stopBG()      // Cancel running lookups
	s.bgWork.Wait() // And wait for them
	s.hub.stop()
}

var partials = []string{"templates/layout.html", "templates/board.html"}

// parseTemplates gives each page its own template set, since every page defines
// its own "content" block.
func parseTemplates() (map[string]*template.Template, error) {
	pages, err := fs.Glob(templateFS, "templates/*.html")
	if err != nil {
		return nil, err
	}
	isPartial := make(map[string]bool, len(partials))
	for _, p := range partials {
		isPartial[p] = true
	}

	out := make(map[string]*template.Template)
	for _, page := range pages {
		if isPartial[page] {
			continue
		}
		name := strings.TrimPrefix(page, "templates/")
		files := append(append([]string{}, partials...), page)

		t, err := template.New(name).Funcs(funcs).ParseFS(templateFS, files...)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", name, err)
		}
		// Content outside every {{define}} is never rendered, so refuse to start.
		for _, f := range files {
			if stray := strayContent(t, strings.TrimPrefix(f, "templates/")); stray != "" {
				return nil, fmt.Errorf("%s: %q is outside every define block", f, stray)
			}
		}
		out[name] = t
	}
	if len(out) == 0 {
		return nil, errors.New("no page templates found")
	}
	return out, nil
}

// strayContent returns what a template file has outside its {{define}} blocks.
func strayContent(set *template.Template, file string) string {
	t := set.Lookup(file)
	if t == nil || t.Tree == nil {
		return ""
	}
	var b strings.Builder
	for _, n := range t.Tree.Root.Nodes {
		b.WriteString(n.String())
	}
	return strings.TrimSpace(b.String())
}

var funcs = template.FuncMap{
	"dict": func(pairs ...any) (map[string]any, error) {
		if len(pairs)%2 != 0 {
			return nil, errors.New("dict needs an even number of arguments")
		}
		out := make(map[string]any, len(pairs)/2)
		for i := 0; i < len(pairs); i += 2 {
			key, ok := pairs[i].(string)
			if !ok {
				return nil, fmt.Errorf("dict key %d is not a string", i)
			}
			out[key] = pairs[i+1]
		}
		return out, nil
	},
	"list": func(items ...string) []string { return items },
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.securityHeaders(s.mux).ServeHTTP(w, r)
}

func (s *Server) routes() {
	mux := http.NewServeMux()

	static, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic(fmt.Sprintf("static fs: %v", err)) // Embedded, cannot fail at runtime
	}

	mux.Handle("GET /static/",
		http.StripPrefix("/static/", cacheControl(http.FileServer(http.FS(static)))))

	posterFS := http.FileServer(http.Dir(s.posters.Dir()))
	mux.Handle("GET /posters/", http.StripPrefix("/posters/", cacheControl(posterFS)))

	mux.HandleFunc("GET /healthz", s.handleHealth)

	mux.HandleFunc("GET  /login", s.handleLoginForm)
	mux.HandleFunc("POST /login", s.handleLogin)
	mux.HandleFunc("GET  /register", s.handleRegisterForm)
	mux.HandleFunc("POST /register", s.handleRegister)
	mux.HandleFunc("POST /logout", s.requireUser(s.handleLogout))
	mux.HandleFunc("GET  /settings", s.requireUser(s.handleSettings))
	mux.HandleFunc("POST /settings/name", s.requireUser(s.handleRename))
	mux.HandleFunc("POST /settings/password", s.requireUser(s.handlePassword))

	mux.HandleFunc("POST /movies", s.requireUser(s.handleAddMovie))
	mux.HandleFunc("POST /movies/{id}/vote", s.requireUser(s.handleVote))
	mux.HandleFunc("POST /movies/{id}/unvote", s.requireUser(s.handleUnvote))
	mux.HandleFunc("POST /movies/{id}/delete", s.requireUser(s.handleDeleteMovie))
	mux.HandleFunc("POST /movies/{id}/watched", s.requireAdmin(s.handleMarkWatched))
	mux.HandleFunc("POST /movies/{id}/reopen", s.requireAdmin(s.handleReopen))

	mux.HandleFunc("GET  /podium", s.optionalUser(s.handlePodium))
	mux.HandleFunc("GET  /podium/{code}", s.optionalUser(s.handlePodium))
	mux.HandleFunc("GET  /podium-top", s.optionalUser(s.handlePodiumTop))
	mux.HandleFunc("GET  /podium-live", s.handlePodiumEvents)

	mux.HandleFunc("GET  /events", s.requireUser(s.handleEvents))
	mux.HandleFunc("GET  /board", s.requireUser(s.handleBoard))

	mux.HandleFunc("GET  /join/{code}", s.handleJoinForm)
	mux.HandleFunc("POST /join/{code}", s.handleJoin)

	mux.HandleFunc("GET  /sessions", s.requireAdmin(s.handleSessions))
	mux.HandleFunc("POST /sessions", s.requireAdmin(s.handleCreateSession))
	mux.HandleFunc("POST /sessions/{id}/close", s.requireAdmin(s.handleCloseSession))
	mux.HandleFunc("POST /sessions/{id}/delete", s.requireAdmin(s.handleDeleteSession))

	mux.HandleFunc("GET  /admin", s.requireAdmin(s.handleAdmin))
	mux.HandleFunc("POST /admin/settings", s.requireAdmin(s.handleAdminSettings))
	mux.HandleFunc("POST /admin/users/{id}/admin", s.requireAdmin(s.handleSetAdmin))
	mux.HandleFunc("POST /admin/users/{id}/delete", s.requireAdmin(s.handleDeleteUser))
	mux.HandleFunc("POST /sessions/{id}/movies/{movie}/watched", s.requireAdmin(s.handleSessionWatched))

	mux.HandleFunc("GET /{$}", s.requireUser(s.handleIndex))
	mux.HandleFunc("/", s.handleNotFound)

	s.mux = mux
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintln(w, `{"status":"ok"}`)
}

const (
	topCount   = 3
	viewCookie = "watchpickr_view"
)

const (
	ViewCards   = "cards"
	ViewCompact = "compact"
	ViewList    = "list"
)

// boardView picks the layout: ?view= if given (remembered in a cookie), else
// the cookie, else cards.
func (s *Server) boardView(w http.ResponseWriter, r *http.Request) string {
	if requested := r.URL.Query().Get("view"); requested != "" {
		view := knownView(requested)
		http.SetCookie(w, &http.Cookie{
			Name: viewCookie, Value: view, Path: "/",
			MaxAge:   int((365 * 24 * time.Hour).Seconds()),
			HttpOnly: true, Secure: s.cfg.SecureCookies, SameSite: http.SameSiteLaxMode,
		})
		return view
	}
	if c, err := r.Cookie(viewCookie); err == nil {
		return knownView(c.Value)
	}
	return ViewCards
}

// knownView turns anything that isn't a view into cards
func knownView(view string) string {
	switch view {
	case ViewCompact, ViewList:
		return view
	}
	return ViewCards
}

// viewURL is the current URL with a different view, keeping the filters.
func viewURL(r *http.Request, view string) string {
	values := r.URL.Query()
	values.Set("view", view)
	return "/?" + values.Encode()
}

func (s *Server) boardData(w http.ResponseWriter, r *http.Request) (map[string]any, error) {
	ctx := r.Context()
	user := userFrom(ctx)

	poll := pollFrom(ctx)
	movies, err := s.store.Movies(ctx, poll.ID, user.ID)
	if err != nil {
		return nil, err
	}
	used, err := s.store.VotesUsed(ctx, poll.ID, user.ID)
	if err != nil {
		return nil, err
	}
	left := poll.MaxVotes - used
	if left < 0 {
		left = 0
	}

	query := browse.ParseQuery(r.URL.Query())
	view := s.boardView(w, r)

	data := map[string]any{
		"Title":      "Board",
		"Poll":       poll,
		"VotesUsed":  used,
		"VotesLeft":  left,
		"OverBy":     max(0, used-poll.MaxVotes),
		"MaxVotes":   poll.MaxVotes,
		"Query":      query,
		"Filtering":  query.Active(),
		"View":       view,
		"IsList":     view == ViewList,
		"IsCompact":  view == ViewCompact,
		"CardsURL":   viewURL(r, ViewCards),
		"CompactURL": viewURL(r, ViewCompact),
		"ListURL":    viewURL(r, ViewList),
		// Built from the board, so every option matches something
		"AllGenres": browse.Genres(movies),
		// So actions return to the same filtered view
		"BackQuery": r.URL.RawQuery,
	}

	// A filtered board is one flat list, without the podium.
	if query.Active() {
		data["Results"] = browse.Apply(movies, query)
		return data, nil
	}

	var top, rest, watched []store.Movie
	for _, m := range movies {
		switch {
		case m.Watched():
			watched = append(watched, m)
		case m.Rank <= topCount && m.Votes > 0:
			top = append(top, m)
		default:
			rest = append(rest, m)
		}
	}

	data["Top"] = top
	data["Rest"] = rest
	data["Watched"] = watched
	// The list and compact views have no podium, so leaders go in with the rest.
	data["Open"] = append(append([]store.Movie{}, top...), rest...)
	return data, nil
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	data, err := s.boardData(w, r)
	if err != nil {
		s.fail(w, r, "could not load the board", err)
		return
	}
	s.render(w, r, http.StatusOK, "index.html", data)
}

func (s *Server) render(w http.ResponseWriter, r *http.Request, status int, page string, data map[string]any) {
	if data == nil {
		data = map[string]any{}
	}
	// Only full pages take the flash, so a live refresh can't swallow it.
	data["Flash"] = s.takeFlash(w, r)
	s.execute(w, r, status, page, "layout", data)
}

// renderBlock sends one named block without the surrounding page
func (s *Server) renderBlock(w http.ResponseWriter, r *http.Request, page, block string, data map[string]any) {
	s.execute(w, r, http.StatusOK, page, block, data)
}

func (s *Server) execute(w http.ResponseWriter, r *http.Request, status int, page, block string, data map[string]any) {
	t, ok := s.tmpl[page]
	if !ok {
		s.log.Error("unknown template", "page", page)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if data == nil {
		data = map[string]any{}
	}
	data["SiteName"] = s.cfg.SiteName
	data["AssetVersion"] = s.assetVersion
	data["User"] = userFrom(r.Context())
	data["LookupEnabled"] = s.meta.Enabled()
	if _, set := data["Poll"]; !set {
		data["Poll"] = pollFrom(r.Context())
	}
	if _, set := data["CSRF"]; !set {
		data["CSRF"] = csrfFrom(r.Context())
	}

	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, block, data); err != nil {
		s.log.Error("render template", "page", page, "block", block, "err", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	buf.WriteTo(w)
}

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "same-origin")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Content-Security-Policy",
			"default-src 'self'; img-src 'self' data:; script-src 'self'; "+
				"style-src 'self'; connect-src 'self'; form-action 'self'; "+
				"base-uri 'none'; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}

// cacheControl caches assets for a year. Safe because their URLs carry a hash.
func cacheControl(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("v") != "" {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "public, max-age=0, must-revalidate")
		}
		next.ServeHTTP(w, r)
	})
}

func hashAssets() (string, error) {
	sum := sha256.New()
	err := fs.WalkDir(staticFS, "static", func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		body, err := staticFS.ReadFile(path)
		if err != nil {
			return err
		}
		sum.Write([]byte(path))
		sum.Write(body)
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("hash assets: %w", err)
	}
	return hex.EncodeToString(sum.Sum(nil))[:12], nil
}

// notFound renders the 404 page.
func (s *Server) notFound(w http.ResponseWriter, r *http.Request, heading, message string) {
	s.render(w, r, http.StatusNotFound, "error.html", map[string]any{
		"Title":   heading,
		"Heading": heading,
		"Message": message,
	})
}

// handleNotFound catches everything the mux did not match.
func (s *Server) handleNotFound(w http.ResponseWriter, r *http.Request) {
	// Keep the header for logged-in users
	if user, csrf := s.session(r); user != nil {
		if poll, err := s.store.PollForUser(r.Context(), user); err == nil {
			r = s.withUser(r, user, csrf, poll)
		}
	}
	s.notFound(w, r, "Page not found", "That link leads nowhere. Try the board.")
}
