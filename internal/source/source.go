// Package source downloads the published JSON files and keeps a local raw
// snapshot of them, so imports can be diffed by sha256 and rebuilt offline.
package source

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/your-github-user/prekladoveklice-mcp/internal/model"
)

const AppDataKey = "appData"

// SectionKey is the manifest key of a section file.
func SectionKey(id int) string { return "section:" + strconv.Itoa(id) }

// File is one downloaded (or cached) source file.
type File struct {
	Key     string
	URL     string
	Body    []byte
	SHA256  string
	ETag    string
	FromRaw bool // served from the raw snapshot (HTTP 304 or offline)
}

// Client fetches source files.
type Client struct {
	BaseURL   string // e.g. https://prekladoveklice.d20.cz/prekladove-klice
	RawDir    string // local snapshot directory
	UserAgent string
	Offline   bool
	HTTP      *http.Client
}

func (c *Client) url(key string) string {
	base := strings.TrimRight(c.BaseURL, "/")
	if key == AppDataKey {
		return base + "/appData.json"
	}
	return base + "/sekce/" + strings.TrimPrefix(key, "section:") + ".json"
}

func (c *Client) rawPath(key string) string {
	if key == AppDataKey {
		return filepath.Join(c.RawDir, "appData.json")
	}
	return filepath.Join(c.RawDir, "sekce", strings.TrimPrefix(key, "section:")+".json")
}

// Fetch returns the file for key. etag is the previously seen ETag; when the
// server answers 304 the raw snapshot is used. Offline mode reads only the
// snapshot.
func (c *Client) Fetch(ctx context.Context, key, etag string) (*File, error) {
	f := &File{Key: key, URL: c.url(key)}
	if c.Offline {
		body, err := os.ReadFile(c.rawPath(key))
		if err != nil {
			return nil, fmt.Errorf("offline: no raw snapshot for %s: %w", key, err)
		}
		f.Body, f.FromRaw, f.ETag = body, true, etag
		f.SHA256 = Sum(body)
		return f, nil
	}

	cached, cacheErr := os.ReadFile(c.rawPath(key))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, f.URL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", c.UserAgent)
	req.Header.Set("Accept", "application/json")
	if etag != "" && cacheErr == nil {
		req.Header.Set("If-None-Match", etag)
	}
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 60 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GET %s: %w", f.URL, err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusNotModified:
		f.Body, f.FromRaw, f.ETag = cached, true, etag
	case http.StatusOK:
		body, err := io.ReadAll(io.LimitReader(resp.Body, 256<<20))
		if err != nil {
			return nil, fmt.Errorf("GET %s: %w", f.URL, err)
		}
		f.Body, f.ETag = body, resp.Header.Get("ETag")
	default:
		return nil, fmt.Errorf("GET %s: HTTP %s", f.URL, resp.Status)
	}
	f.SHA256 = Sum(f.Body)
	return f, nil
}

// SaveRaw writes the files into the raw snapshot directory (atomically per file).
func (c *Client) SaveRaw(files []*File) error {
	for _, f := range files {
		p := c.rawPath(f.Key)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		tmp := p + ".tmp"
		if err := os.WriteFile(tmp, f.Body, 0o644); err != nil {
			return err
		}
		if err := os.Rename(tmp, p); err != nil {
			return err
		}
	}
	return nil
}

// RemoveRaw deletes snapshot files of sections that no longer exist.
func (c *Client) RemoveRaw(keys []string) {
	for _, k := range keys {
		_ = os.Remove(c.rawPath(k))
	}
}

// Sum returns the hex sha256 of b.
func Sum(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// ParseAppData decodes appData.json and sanity-checks it.
func ParseAppData(b []byte) (*model.AppData, error) {
	var a model.AppData
	if err := json.Unmarshal(b, &a); err != nil {
		return nil, fmt.Errorf("appData.json: %w", err)
	}
	if len(a.Sections) == 0 {
		return nil, fmt.Errorf("appData.json: no sections")
	}
	return &a, nil
}

// ParseSection decodes sekce/<id>.json and checks every entry belongs to id.
func ParseSection(id int, b []byte) ([]model.RawEntry, error) {
	var es []model.RawEntry
	if err := json.Unmarshal(b, &es); err != nil {
		return nil, fmt.Errorf("section %d: %w", id, err)
	}
	for _, e := range es {
		if e.ID == 0 || e.Original == "" {
			return nil, fmt.Errorf("section %d: entry without id or original: %+v", id, e)
		}
		if e.Section != id {
			return nil, fmt.Errorf("section %d: entry %d claims section %d", id, e.ID, e.Section)
		}
	}
	return es, nil
}
