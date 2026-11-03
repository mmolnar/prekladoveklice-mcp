// Package backend opens the store selected by the configuration.
package backend

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/mmolnar/prekladoveklice-mcp/internal/config"
	"github.com/mmolnar/prekladoveklice-mcp/internal/source"
	"github.com/mmolnar/prekladoveklice-mcp/internal/store"
	"github.com/mmolnar/prekladoveklice-mcp/internal/store/sqlite"
	"github.com/mmolnar/prekladoveklice-mcp/internal/version"
)

// Open returns the configured store.
func Open(cfg *config.Config) (store.Store, error) {
	switch cfg.Backend {
	case "sqlite":
		return sqlite.Open(cfg.SQLite.Path)
	case "postgres":
		return nil, errors.New("the postgres backend is not implemented yet")
	default:
		return nil, fmt.Errorf("unknown backend %q", cfg.Backend)
	}
}

// Source returns the source client for cfg.
func Source(cfg *config.Config, offline bool) *source.Client {
	ua := cfg.Source.UserAgent
	if ua == "" {
		ua = version.UserAgent()
	}
	return &source.Client{
		BaseURL:   cfg.Source.BaseURL,
		RawDir:    cfg.RawDir(),
		UserAgent: ua,
		Offline:   offline,
		HTTP:      &http.Client{Timeout: 60 * time.Second},
	}
}
