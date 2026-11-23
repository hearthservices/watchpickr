// Package poster caches film posters on local disk.
package poster

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// maxBytes stops a hostile or broken host from filling the disk
const maxBytes = 5 << 20 // 5 MiB

type Cache struct {
	dir  string
	http *http.Client
}

func NewCache(dir string, hc *http.Client) (*Cache, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create poster dir: %w", err)
	}
	if hc == nil {
		hc = &http.Client{Timeout: 20 * time.Second}
	}
	return &Cache{dir: dir, http: hc}, nil
}

func (c *Cache) Dir() string { return c.dir }

// Save downloads a poster and returns its filename, which is based on the IMDb
// id so re-adding a film reuses the file.
func (c *Cache) Save(ctx context.Context, imdbID, url string) (string, error) {
	if url == "" {
		return "", nil
	}
	name := imdbID + ".jpg"

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	res, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch poster: %w", err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("fetch poster: %s", res.Status)
	}
	if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, "image/") {
		return "", fmt.Errorf("fetch poster: content type %q is not an image", ct)
	}

	// Write to a temp file and rename it, so a half-written poster is never served.
	tmp, err := os.CreateTemp(c.dir, ".poster-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name()) // No-op once it's renamed

	if _, err := io.Copy(tmp, io.LimitReader(res.Body, maxBytes)); err != nil {
		tmp.Close()
		return "", fmt.Errorf("write poster: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(tmp.Name(), filepath.Join(c.dir, name)); err != nil {
		return "", fmt.Errorf("store poster: %w", err)
	}
	return name, nil
}
