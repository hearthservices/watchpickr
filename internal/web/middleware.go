package web

import (
	"context"
	"crypto/subtle"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"

	"github.com/hearthservices/watchpickr/internal/auth"
	"github.com/hearthservices/watchpickr/internal/store"
)

const (
	sessionCookie  = "watchpickr_session"
	anonCSRFCookie = "watchpickr_csrf"
)

type ctxKey int

const (
	ctxUser ctxKey = iota
	ctxCSRF
	ctxPoll
)

func userFrom(ctx context.Context) *store.User {
	u, _ := ctx.Value(ctxUser).(*store.User)
	return u
}

func csrfFrom(ctx context.Context) string {
	t, _ := ctx.Value(ctxCSRF).(string)
	return t
}

func pollFrom(ctx context.Context) *store.Poll {
	p, _ := ctx.Value(ctxPoll).(*store.Poll)
	return p
}

// session returns the logged-in user, or nil.
func (s *Server) session(r *http.Request) (*store.User, string) {
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return nil, ""
	}
	user, csrf, err := s.store.Session(r.Context(), c.Value)
	if err != nil {
		return nil, ""
	}
	return user, csrf
}

func (s *Server) withUser(r *http.Request, u *store.User, csrf string, p *store.Poll) *http.Request {
	ctx := context.WithValue(r.Context(), ctxUser, u)
	ctx = context.WithValue(ctx, ctxCSRF, csrf)
	ctx = context.WithValue(ctx, ctxPoll, p)
	return r.WithContext(ctx)
}

// safeMethod reports whether a request only reads. GET patterns match HEAD too,
// so HEAD must count as safe or it fails the CSRF check.
func safeMethod(m string) bool {
	return m == http.MethodGet || m == http.MethodHead
}

// requireUser requires a session, and a CSRF token for anything that writes.
func (s *Server) requireUser(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		user, csrf := s.session(r)
		if user == nil {
			if safeMethod(r.Method) {
				http.Redirect(w, r, "/login", http.StatusSeeOther)
				return
			}
			http.Error(w, "please log in again", http.StatusUnauthorized)
			return
		}
		poll, err := s.store.PollForUser(r.Context(), user)
		if err != nil {
			s.log.Error("resolve poll", "user", user.ID, "err", err)
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		r = s.withUser(r, user, csrf, poll)

		if !safeMethod(r.Method) && !checkCSRF(r, csrf) {
			http.Error(w, "expired form token, reload the page", http.StatusForbidden)
			return
		}
		next(w, r)
	}
}

// optionalUser attaches the user if logged in and lets anyone else through.
// Mostly for showing the podium on a TV.
func (s *Server) optionalUser(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if user, csrf := s.session(r); user != nil {
			poll, err := s.store.PollForUser(r.Context(), user)
			if err != nil {
				s.log.Error("resolve poll", "user", user.ID, "err", err)
			} else {
				r = s.withUser(r, user, csrf, poll)
			}
		}
		next(w, r)
	}
}

func (s *Server) requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return s.requireUser(func(w http.ResponseWriter, r *http.Request) {
		if u := userFrom(r.Context()); u == nil || !u.IsAdmin || u.Kind == store.KindGuest {
			http.Error(w, "administrators only", http.StatusForbidden)
			return
		}
		next(w, r)
	})
}

func checkCSRF(r *http.Request, want string) bool {
	if want == "" {
		return false
	}
	got := r.FormValue("csrf")
	if got == "" {
		got = r.Header.Get("X-CSRF-Token")
	}
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

/* --------------------------------------------------- anonymous form CSRF --- */

// anonCSRF returns a CSRF token for the login and register forms, which have no
// session yet. Login CSRF lets an attacker sign you into their account. Is it
// relevant for a self-hosted movie picker? Unlikely. Is it good hygiene to harden it
// regardless? Yes.
func (s *Server) anonCSRF(w http.ResponseWriter, r *http.Request) (string, error) {
	if c, err := r.Cookie(anonCSRFCookie); err == nil && c.Value != "" {
		return c.Value, nil
	}
	token, err := auth.Token()
	if err != nil {
		return "", err
	}
	http.SetCookie(w, &http.Cookie{
		Name:     anonCSRFCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   s.cfg.SecureCookies,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(time.Hour.Seconds()),
	})
	return token, nil
}

func (s *Server) checkAnonCSRF(r *http.Request) bool {
	c, err := r.Cookie(anonCSRFCookie)
	if err != nil || c.Value == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(r.FormValue("csrf")), []byte(c.Value)) == 1
}

/* ----------------------------------------------------------- client IP --- */

// clientIP is what the rate limiter keys on. X-Forwarded-For only counts when
// the request comes from a trusted proxy, otherwise anyone could fake their IP.
func clientIP(r *http.Request, trusted []netip.Prefix) string {
	peer, err := netip.ParseAddrPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	addr := peer.Addr().Unmap()
	if !trustedAddr(addr, trusted) {
		return addr.String()
	}

	// Proxies append, so the right-most untrusted address is the client.
	parts := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
	for i := len(parts) - 1; i >= 0; i-- {
		candidate, err := netip.ParseAddr(strings.TrimSpace(parts[i]))
		if err != nil {
			continue
		}
		if candidate = candidate.Unmap(); !trustedAddr(candidate, trusted) {
			return candidate.String()
		}
	}

	return addr.String()
}

func trustedAddr(addr netip.Addr, trusted []netip.Prefix) bool {
	for _, p := range trusted {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

/* ------------------------------------------------------------- throttle --- */

// throttle is a simple fixed-window rate limiter.
type throttle struct {
	mu     sync.Mutex
	hits   map[string]*window
	limit  int
	period time.Duration
}

type window struct {
	count int
	reset time.Time
}

func newThrottle(limit int, period time.Duration) *throttle {
	return &throttle{hits: make(map[string]*window), limit: limit, period: period}
}

func (t *throttle) allow(key string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()

	now := time.Now()
	for k, w := range t.hits {
		if now.After(w.reset) { // Clean up old entries while we're here
			delete(t.hits, k)
		}
	}
	w, ok := t.hits[key]
	if !ok || now.After(w.reset) {
		t.hits[key] = &window{count: 1, reset: now.Add(t.period)}
		return true
	}
	w.count++
	return w.count <= t.limit
}

// clear resets one key. Only use it for the user who just logged in, or anyone
// could reset the counter for someone else's account.
func (t *throttle) clear(key string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.hits, key)
}
