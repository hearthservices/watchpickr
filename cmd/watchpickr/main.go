// Command watchpickr serves the movie voting board.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/hearthservices/watchpickr/internal/auth"
	"github.com/hearthservices/watchpickr/internal/config"
	"github.com/hearthservices/watchpickr/internal/demo"
	"github.com/hearthservices/watchpickr/internal/metadata"
	"github.com/hearthservices/watchpickr/internal/poster"
	"github.com/hearthservices/watchpickr/internal/store"
	"github.com/hearthservices/watchpickr/internal/web"
)

// version is stamped at build time with -ldflags "-X main.version=..."
var version = "dev"

func main() {
	demoFlag := flag.Bool("demo", false,
		"run a throwaway demo: temporary database, seeded data, discarded on exit")
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stdout, nil))
	if err := run(log, *demoFlag); err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger, demoFlag bool) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	if demoFlag || cfg.Demo {
		dir, err := os.MkdirTemp("", "watchpickr-demo-*")
		if err != nil {
			return err
		}
		defer os.RemoveAll(dir)

		cfg.DBPath = filepath.Join(dir, "demo.db")
		cfg.PosterDir = filepath.Join(dir, "posters")
		cfg.AdminPassword = demo.Password
		cfg.RegistrationCode = ""
		log.Warn("demo mode: throwaway database, discarded on exit", "dir", dir)
	}

	if dir := filepath.Dir(cfg.DBPath); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}

	st, err := store.Open(cfg.DBPath)
	if err != nil {
		return err
	}
	defer st.Close()

	if err := bootstrapAdmin(context.Background(), st, cfg, log); err != nil {
		return err
	}

	posters, err := poster.NewCache(cfg.PosterDir, nil)
	if err != nil {
		return err
	}

	if demoFlag || cfg.Demo {
		if err := demo.Seed(context.Background(), st, posters); err != nil {
			return fmt.Errorf("seed demo data: %w", err)
		}
		log.Info("demo data ready",
			"sign_in_as", "admin, Anna, Benny, Cecilia, or David",
			"password", demo.Password,
			"open_session_code", demo.JoinCode)
	}

	meta := metadata.New(cfg.TMDbAPIKey, cfg.OMDbAPIKey, nil)
	if meta.Enabled() {
		log.Info("film lookup enabled", "sources", meta.Sources())

		vctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		for _, err := range meta.Verify(vctx) {
			log.Warn("film lookup will be incomplete", "err", err,
				"hint", "a free OMDb key must be activated from the link in the signup email")
		}
		cancel()
	} else {
		log.Warn("film lookup disabled: set TMDB_API_KEY to fill in posters and ratings",
			"get_a_free_key", "https://www.themoviedb.org/settings/api")
	}

	srv, err := web.New(cfg, st, meta, posters, log)
	if err != nil {
		return err
	}

	httpSrv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           srv,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		IdleTimeout:       90 * time.Second,
		// No WriteTimeout, it would cut off the SSE streams.
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go reapSessions(ctx, st, log)

	errc := make(chan error, 1)
	go func() {
		log.Info("watchpickr listening", "version", version, "addr", cfg.Addr, "db", cfg.DBPath)
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errc <- err
		}
	}()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		log.Info("shutting down")
	}

	// Close the event streams first, or Shutdown sits here prettily waiting for
	// them until it times out.
	srv.Close()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return httpSrv.Shutdown(shutdownCtx)
}

// bootstrapAdmin creates the admin account, generating a password if none is set.
func bootstrapAdmin(ctx context.Context, st *store.Store, cfg config.Config, log *slog.Logger) error {
	exists, err := st.AdminExists(ctx)
	if err != nil {
		return err
	}
	if exists && cfg.AdminPassword == "" {
		return nil // Already set up
	}

	password := cfg.AdminPassword
	generated := password == ""
	if generated {
		token, err := auth.Token()
		if err != nil {
			return err
		}
		password = token[:16]
	} else if err := auth.ValidatePassword(password); err != nil {
		return fmt.Errorf("WATCHPICKR_ADMIN_PASSWORD: %w", err)
	}

	hash, err := auth.HashPassword(password)
	if err != nil {
		return err
	}
	if err := st.EnsureAdmin(ctx, cfg.AdminUsername, hash); err != nil {
		return err
	}

	if generated {
		log.Warn("generated an admin password -- save it now, it is not shown again",
			"username", cfg.AdminUsername, "password", password)
	} else {
		log.Info("admin password applied from the environment", "username", cfg.AdminUsername)
	}
	return nil
}

// reapSessions clears out expired logins so the table does not grow forever
func reapSessions(ctx context.Context, st *store.Store, log *slog.Logger) {
	ticker := time.NewTicker(6 * time.Hour)
	defer ticker.Stop()
	for {
		if n, err := st.DeleteExpiredSessions(ctx); err != nil {
			if ctx.Err() == nil {
				log.Warn("session cleanup failed", "err", err)
			}
		} else if n > 0 {
			log.Info("expired sessions removed", "count", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
