// Package config reads deployment settings from the environment.
package config

import (
	"fmt"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"time"
)

// defaultTrustedProxies covers loopback and the private ranges.
const defaultTrustedProxies = "127.0.0.0/8,::1/128,10.0.0.0/8,172.16.0.0/12,192.168.0.0/16,fc00::/7"

type Config struct {
	Addr             string
	DBPath           string
	SiteName         string
	SecureCookies    bool
	SessionTTL       time.Duration
	AdminUsername    string
	AdminPassword    string
	RegistrationCode string
	TrustedProxies   []netip.Prefix
	TMDbAPIKey       string
	OMDbAPIKey       string
	PosterDir        string
	Demo             bool
}

func Load() (Config, error) {
	cfg := Config{
		Addr:             env("WATCHPICKR_ADDR", ":8080"),
		DBPath:           env("WATCHPICKR_DB", "data/watchpickr.db"),
		SiteName:         env("WATCHPICKR_SITE_NAME", "WatchPickr"),
		SecureCookies:    envBool("WATCHPICKR_SECURE_COOKIES", false),
		SessionTTL:       time.Duration(envInt("WATCHPICKR_SESSION_DAYS", 30)) * 24 * time.Hour,
		AdminUsername:    env("WATCHPICKR_ADMIN_USERNAME", "admin"),
		AdminPassword:    os.Getenv("WATCHPICKR_ADMIN_PASSWORD"),
		RegistrationCode: strings.TrimSpace(os.Getenv("WATCHPICKR_REGISTRATION_CODE")),
		TMDbAPIKey:       strings.TrimSpace(os.Getenv("TMDB_API_KEY")),
		OMDbAPIKey:       strings.TrimSpace(os.Getenv("OMDB_API_KEY")),
		PosterDir:        env("WATCHPICKR_POSTER_DIR", "data/posters"),
		Demo:             envBool("WATCHPICKR_DEMO", false),
	}

	proxies, err := parsePrefixes(env("WATCHPICKR_TRUSTED_PROXIES", defaultTrustedProxies))
	if err != nil {
		return Config{}, fmt.Errorf("WATCHPICKR_TRUSTED_PROXIES: %w", err)
	}
	cfg.TrustedProxies = proxies

	return cfg, nil
}

func parsePrefixes(list string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, raw := range strings.Split(list, ",") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		p, err := netip.ParsePrefix(raw)
		if err != nil {
			return nil, fmt.Errorf("%q is not a CIDR range: %w", raw, err)
		}
		out = append(out, p)
	}
	return out, nil
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envBool(key string, fallback bool) bool {
	v, err := strconv.ParseBool(os.Getenv(key))
	if err != nil {
		return fallback
	}
	return v
}

func envInt(key string, fallback int) int {
	v, err := strconv.Atoi(os.Getenv(key))
	if err != nil || v <= 0 {
		return fallback
	}
	return v
}
