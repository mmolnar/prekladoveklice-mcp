// Package mcpserver exposes the store as MCP tools over stdio or Streamable HTTP.
package mcpserver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mmolnar/prekladoveklice-mcp/internal/model"
	"github.com/mmolnar/prekladoveklice-mcp/internal/store"
	"github.com/mmolnar/prekladoveklice-mcp/internal/version"
)

const maxLimit = 100

const instructions = `Czech translation keys for tabletop RPGs (D&D editions, Forgotten Realms, Fate, ...) from prekladoveklice.d20.cz.
Use search_keys to find the official/community Czech translation of an English term (or the English original of a Czech one with field="translation").
Matching ignores case and diacritics. Default mode is substring; use mode="exact" for a precise term, mode="fts" for word queries (OR, "phrases", prefix*).
The same term often has different translations per game edition: restrict with sections (see list_sections), and tags (see list_tags) such as spells or source books.
Deprecated translations are hidden unless include_deprecated is true.`

// SearchInput are the search_keys arguments.
type SearchInput struct {
	Query             string   `json:"query,omitempty" jsonschema:"text to look for; may be empty when filtering by section/tag only"`
	Mode              string   `json:"mode,omitempty" jsonschema:"substring (default), exact, prefix, or fts (full-text: words, \"phrases\", OR, prefix*)"`
	Field             string   `json:"field,omitempty" jsonschema:"both (default: English original and Czech translation), original, translation, or all (also notes)"`
	Sections          []string `json:"sections,omitempty" jsonschema:"section names or ids to search in, e.g. [\"D&D 5e\"]"`
	Tags              []string `json:"tags,omitempty" jsonschema:"tag or book names/ids; an entry must match every given tag"`
	IncludeDeprecated bool     `json:"include_deprecated,omitempty" jsonschema:"also return deprecated (superseded) translations"`
	Limit             int      `json:"limit,omitempty" jsonschema:"max results, default 20, max 100"`
	Offset            int      `json:"offset,omitempty" jsonschema:"skip this many results (paging)"`
}

type GetInput struct {
	ID int `json:"id" jsonschema:"entry id from search_keys"`
}

type SectionsOutput struct {
	Sections []model.Section `json:"sections"`
}

type TagsInput struct {
	Section string `json:"section,omitempty" jsonschema:"section name or id to list tags of"`
	Type    string `json:"type,omitempty" jsonschema:"tag or book"`
}

type TagsOutput struct {
	Tags []model.Tag `json:"tags"`
}

// New builds the MCP server for st.
func New(st store.Store) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "pk", Title: "Překladové klíče", Version: version.Version},
		&mcp.ServerOptions{Instructions: instructions})
	ro := &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: ptr(false)}

	mcp.AddTool(s, &mcp.Tool{Name: "search_keys", Annotations: ro,
		Description: "Search English→Czech RPG translation keys, optionally filtered by section and tag. Returns total match count and a page of entries."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in SearchInput) (*mcp.CallToolResult, *model.SearchResult, error) {
			q := model.Query{Text: in.Query, Mode: model.Mode(in.Mode), Field: model.Field(in.Field),
				IncludeDeprecated: in.IncludeDeprecated, Limit: in.Limit, Offset: in.Offset}
			var err error
			if q.SectionIDs, err = store.ResolveSections(ctx, st, in.Sections); err != nil {
				return nil, nil, err
			}
			if q.TagGroups, err = store.ResolveTags(ctx, st, in.Tags, q.SectionIDs); err != nil {
				return nil, nil, err
			}
			if q.Limit > maxLimit {
				q.Limit = maxLimit
			}
			res, err := st.Search(ctx, q)
			return nil, res, err
		})

	mcp.AddTool(s, &mcp.Tool{Name: "get_key", Annotations: ro,
		Description: "Get one translation key by id, with notes, tags and its deprecated translations."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in GetInput) (*mcp.CallToolResult, *model.Entry, error) {
			e, err := st.Get(ctx, in.ID)
			if errors.Is(err, store.ErrNotFound) {
				err = fmt.Errorf("no entry with id %d", in.ID)
			}
			return nil, e, err
		})

	mcp.AddTool(s, &mcp.Tool{Name: "list_sections", Annotations: ro,
		Description: "List sections (dictionaries such as game editions and settings) with ids and entry counts."},
		func(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, *SectionsOutput, error) {
			ss, err := st.Sections(ctx)
			return nil, &SectionsOutput{Sections: ss}, err
		})

	mcp.AddTool(s, &mcp.Tool{Name: "list_tags", Annotations: ro,
		Description: "List tags (topics like spells or skills) and books, optionally for one section."},
		func(ctx context.Context, _ *mcp.CallToolRequest, in TagsInput) (*mcp.CallToolResult, *TagsOutput, error) {
			f := model.TagFilter{Type: in.Type}
			if in.Section != "" {
				ids, err := store.ResolveSections(ctx, st, []string{in.Section})
				if err != nil {
					return nil, nil, err
				}
				f.SectionIDs = ids
			}
			ts, err := st.Tags(ctx, f)
			return nil, &TagsOutput{Tags: ts}, err
		})
	return s
}

// ServeStdio serves one client over stdin/stdout.
func ServeStdio(ctx context.Context, s *mcp.Server) error {
	return s.Run(ctx, &mcp.StdioTransport{})
}

// ServeHTTP serves Streamable HTTP at path on addr until ctx is done.
func ServeHTTP(ctx context.Context, s *mcp.Server, addr, path string, log *slog.Logger) error {
	mux := http.NewServeMux()
	mux.Handle(path, mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s }, nil))
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok\n")) })
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	log.Info("serving MCP over HTTP", "addr", addr, "path", path)
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(shutdown)
	}
}

func ptr[T any](v T) *T { return &v }
