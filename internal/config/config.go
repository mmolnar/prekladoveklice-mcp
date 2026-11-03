// Package config loads pk's YAML configuration, with environment overrides.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// Config is the full configuration. Every field has a working default, so
// no file is needed for local use.
type Config struct {
	Backend  string   `yaml:"backend"`  // "sqlite" or "postgres"
	DataDir  string   `yaml:"data_dir"` // raw snapshots, lock file, default SQLite file
	SQLite   SQLite   `yaml:"sqlite"`
	Postgres Postgres `yaml:"postgres"`
	Source   Source   `yaml:"source"`
	MCP      MCP      `yaml:"mcp"`

	// File is the config file that was loaded ("" when none).
	File string `yaml:"-"`
}

type SQLite struct {
	Path string `yaml:"path"` // default <data_dir>/keys.db
}

type Postgres struct {
	DSN    string `yaml:"dsn"`    // e.g. postgres://user:pass@host/db
	Schema string `yaml:"schema"` // live schema name, default "pk"
}

type Source struct {
	BaseURL   string `yaml:"base_url"`
	UserAgent string `yaml:"user_agent"`
}

type MCP struct {
	Transport string `yaml:"transport"` // "stdio" or "http"
	Listen    string `yaml:"listen"`    // http: address, default 127.0.0.1:8080
	Path      string `yaml:"path"`      // http: endpoint path, default /mcp
	// RefreshInterval re-runs the import periodically while serving (0 = off).
	RefreshInterval Duration `yaml:"refresh_interval"`
	// ImportOnStart imports before serving when the store is empty.
	ImportOnStart bool `yaml:"import_on_start"`
}

// Duration is a time.Duration written as "24h", "90m", ...
type Duration struct{ time.Duration }

func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	if n.Value == "" || n.Value == "0" {
		d.Duration = 0
		return nil
	}
	v, err := time.ParseDuration(n.Value)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", n.Value, err)
	}
	d.Duration = v
	return nil
}

func (d Duration) MarshalYAML() (any, error) {
	if d.Duration == 0 {
		return "0", nil
	}
	return d.String(), nil
}

const DefaultBaseURL = "https://prekladoveklice.d20.cz/prekladove-klice"

// Load reads the config file at path, or if path is empty the first of
// $PK_CONFIG, ./pk.yaml and <user config dir>/pk/config.yaml that exists.
// Values may reference environment variables as ${VAR}. PK_BACKEND,
// PK_DATA_DIR, PK_SQLITE_PATH and PK_POSTGRES_DSN override the file.
func Load(path string) (*Config, error) {
	c := &Config{}
	candidates := []string{path}
	if path == "" {
		candidates = []string{os.Getenv("PK_CONFIG"), "pk.yaml"}
		if dir, err := os.UserConfigDir(); err == nil {
			candidates = append(candidates, filepath.Join(dir, "pk", "config.yaml"))
		}
	}
	for _, p := range candidates {
		if p == "" {
			continue
		}
		b, err := os.ReadFile(p)
		if errors.Is(err, fs.ErrNotExist) && path == "" {
			continue
		}
		if err != nil {
			return nil, err
		}
		if err := yaml.Unmarshal([]byte(os.ExpandEnv(string(b))), c); err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}
		c.File = p
		break
	}
	envOverride(&c.Backend, "PK_BACKEND")
	envOverride(&c.DataDir, "PK_DATA_DIR")
	envOverride(&c.SQLite.Path, "PK_SQLITE_PATH")
	envOverride(&c.Postgres.DSN, "PK_POSTGRES_DSN")
	if err := c.fillDefaults(); err != nil {
		return nil, err
	}
	return c, c.Validate()
}

func envOverride(dst *string, name string) {
	if v := os.Getenv(name); v != "" {
		*dst = v
	}
}

func (c *Config) fillDefaults() error {
	if c.Backend == "" {
		c.Backend = "sqlite"
	}
	if c.DataDir == "" {
		d, err := DefaultDataDir()
		if err != nil {
			return err
		}
		c.DataDir = d
	}
	c.DataDir = expandHome(c.DataDir)
	if c.SQLite.Path == "" {
		c.SQLite.Path = filepath.Join(c.DataDir, "keys.db")
	}
	c.SQLite.Path = expandHome(c.SQLite.Path)
	if c.Postgres.Schema == "" {
		c.Postgres.Schema = "pk"
	}
	if c.Source.BaseURL == "" {
		c.Source.BaseURL = DefaultBaseURL
	}
	if c.MCP.Transport == "" {
		c.MCP.Transport = "stdio"
	}
	if c.MCP.Listen == "" {
		c.MCP.Listen = "127.0.0.1:8080"
	}
	if c.MCP.Path == "" {
		c.MCP.Path = "/mcp"
	}
	return nil
}

// Validate checks enumerated values.
func (c *Config) Validate() error {
	switch c.Backend {
	case "sqlite":
	case "postgres":
		if c.Postgres.DSN == "" {
			return errors.New("backend postgres needs postgres.dsn (or PK_POSTGRES_DSN)")
		}
	default:
		return fmt.Errorf("unknown backend %q (sqlite, postgres)", c.Backend)
	}
	switch c.MCP.Transport {
	case "stdio", "http":
	default:
		return fmt.Errorf("unknown mcp.transport %q (stdio, http)", c.MCP.Transport)
	}
	return nil
}

// RawDir is where source JSON snapshots are kept.
func (c *Config) RawDir() string { return filepath.Join(c.DataDir, "raw") }

// LockPath is the import lock file.
func (c *Config) LockPath() string { return filepath.Join(c.DataDir, "import.lock") }

// DefaultDataDir is $XDG_DATA_HOME/pk, ~/.local/share/pk, or %LOCALAPPDATA%\pk on Windows.
func DefaultDataDir() (string, error) {
	if runtime.GOOS == "windows" {
		if d := os.Getenv("LOCALAPPDATA"); d != "" {
			return filepath.Join(d, "pk"), nil
		}
	}
	if d := os.Getenv("XDG_DATA_HOME"); d != "" {
		return filepath.Join(d, "pk"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "share", "pk"), nil
}

func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			return filepath.Join(home, p[1:])
		}
	}
	return p
}

// Example is a commented sample configuration (printed by `pk config --example`).
const Example = `# pk configuration. Every setting is optional.
# Values may use ${ENV_VAR}. Env overrides: PK_BACKEND, PK_DATA_DIR,
# PK_SQLITE_PATH, PK_POSTGRES_DSN. Lookup order: --config, $PK_CONFIG,
# ./pk.yaml, <user config dir>/pk/config.yaml.

backend: sqlite              # sqlite | postgres
# data_dir: ~/.local/share/pk  # raw JSON snapshots, import lock, default SQLite file

sqlite:
  # path: ~/.local/share/pk/keys.db

postgres:
  dsn: ${PK_POSTGRES_DSN}    # postgres://user:password@host:5432/dbname
  schema: pk

source:
  base_url: https://prekladoveklice.d20.cz/prekladove-klice
  # user_agent: my-mirror (contact@example.com)

mcp:
  transport: stdio           # stdio (local clients) | http (Streamable HTTP server)
  listen: 127.0.0.1:8080     # http only
  path: /mcp                 # http only
  refresh_interval: 0        # e.g. 24h: re-import periodically while serving
  import_on_start: false     # import before serving if there is no data yet
`
