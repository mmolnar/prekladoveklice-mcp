// Package cli implements the pk command line.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"go.yaml.in/yaml/v3"

	"github.com/mmolnar/prekladoveklice-mcp/internal/backend"
	"github.com/mmolnar/prekladoveklice-mcp/internal/config"
	"github.com/mmolnar/prekladoveklice-mcp/internal/importer"
	"github.com/mmolnar/prekladoveklice-mcp/internal/mcpserver"
	"github.com/mmolnar/prekladoveklice-mcp/internal/model"
	"github.com/mmolnar/prekladoveklice-mcp/internal/store"
	"github.com/mmolnar/prekladoveklice-mcp/internal/version"
)

type app struct {
	configPath string
	dbPath     string
	asJSON     bool
	cfg        *config.Config
	out        io.Writer
	errOut     io.Writer
}

// Execute runs the CLI and returns the process exit code.
func Execute() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	a := &app{out: os.Stdout, errOut: os.Stderr}
	root := a.root()
	if err := root.ExecuteContext(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "pk:", err)
		return 1
	}
	return 0
}

func (a *app) root() *cobra.Command {
	root := &cobra.Command{
		Use:   "pk",
		Short: "Search Czech RPG translation keys from prekladoveklice.d20.cz",
		Long: `pk keeps a local, searchable copy of the translation keys published at
https://prekladoveklice.d20.cz/ and serves them on the command line or over MCP.

Start with "pk import", then e.g. "pk search fireball".`,
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := config.Load(a.configPath)
			if err != nil {
				return err
			}
			if a.dbPath != "" {
				cfg.Backend, cfg.SQLite.Path = "sqlite", a.dbPath
			}
			a.cfg = cfg
			return nil
		},
	}
	pf := root.PersistentFlags()
	pf.StringVar(&a.configPath, "config", "", "config file (default: $PK_CONFIG, ./pk.yaml, <user config dir>/pk/config.yaml)")
	pf.StringVar(&a.dbPath, "db", "", "use this SQLite database file (overrides config)")
	pf.BoolVar(&a.asJSON, "json", false, "print JSON")
	root.AddCommand(a.importCmd(), a.statusCmd(), a.sectionsCmd(), a.tagsCmd(), a.searchCmd(),
		a.showCmd(), a.mcpCmd(), a.configCmd(), a.versionCmd())
	return root
}

func (a *app) open() (store.Store, error) { return backend.Open(a.cfg) }

func (a *app) printJSON(v any) error {
	enc := json.NewEncoder(a.out)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

func (a *app) importCmd() *cobra.Command {
	var opts importer.Options
	var offline, quiet bool
	cmd := &cobra.Command{
		Use:   "import",
		Short: "Download the latest keys and update the local database",
		Long: `Downloads appData.json and every section, compares sha256 checksums with the
previous import and rewrites only the sections that changed. The new data is
built next to the live database and verified (entry counts per section,
integrity checks, a test search) before it replaces the live data; the
previous version is kept as a backup. On any failure the live data is untouched.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			st, err := a.open()
			if err != nil {
				return err
			}
			defer st.Close()
			opts.LockPath = a.cfg.LockPath()
			if !quiet && !a.asJSON {
				opts.Log = func(f string, args ...any) { fmt.Fprintf(a.errOut, f+"\n", args...) }
			}
			start := time.Now()
			rep, err := importer.Run(cmd.Context(), st, backend.Source(a.cfg, offline), opts)
			if a.asJSON && rep != nil {
				a.printJSON(rep)
			}
			if err != nil {
				return err
			}
			if !a.asJSON {
				printReport(a.out, rep, time.Since(start))
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.BoolVar(&opts.Rebuild, "rebuild", false, "rebuild everything instead of only changed sections")
	f.BoolVar(&offline, "offline", false, "use the local raw snapshot instead of downloading")
	f.BoolVar(&opts.DryRun, "dry-run", false, "build and verify, but keep the current data")
	f.BoolVar(&opts.AllowShrink, "allow-shrink", false, fmt.Sprintf("accept an update that removes more than %.0f%% of entries", importer.MaxShrink*100))
	f.BoolVarP(&quiet, "quiet", "q", false, "no progress output")
	return cmd
}

func printReport(w io.Writer, r *importer.Report, took time.Duration) {
	switch {
	case r.UpToDate:
		fmt.Fprintf(w, "Up to date: %d sections, %d entries (%d downloaded, %d not modified).\n",
			r.Sections, r.Entries, r.Downloaded, r.NotModified)
		return
	case r.DryRun:
		fmt.Fprint(w, "Dry run OK, nothing changed. ")
	case r.Fresh:
		fmt.Fprint(w, "Rebuilt database. ")
	default:
		fmt.Fprint(w, "Updated. ")
	}
	fmt.Fprintf(w, "%d sections, %d entries (was %d); changed sections: %s; removed: %s; metadata changed: %v. Took %s.\n",
		r.Sections, r.Entries, r.PreviousEntries, ints(r.ChangedSections), ints(r.RemovedSections), r.MetaChanged, took.Round(time.Millisecond))
	for _, w2 := range r.Warnings {
		fmt.Fprintln(w, "warning:", w2)
	}
}

func ints(xs []int) string {
	if len(xs) == 0 {
		return "none"
	}
	s := make([]string, len(xs))
	for i, x := range xs {
		s[i] = strconv.Itoa(x)
	}
	return strings.Join(s, ",")
}

func (a *app) statusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Show where the data lives and when it was last imported",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			st, err := a.open()
			if err != nil {
				return err
			}
			defer st.Close()
			s, err := st.Status(cmd.Context())
			if errors.Is(err, store.ErrNoData) {
				fmt.Fprintf(a.out, "%s %s: %v\n", s.Backend, s.Location, err)
				return nil
			}
			if err != nil {
				return err
			}
			if a.asJSON {
				return a.printJSON(s)
			}
			fmt.Fprintf(a.out, "backend:     %s\nlocation:    %s\nconfig:      %s\nlast import: %s\nsections:    %d\ntags:        %d\nentries:     %d (+%d deprecated)\n",
				s.Backend, s.Location, orNone(a.cfg.File), s.LastImport, s.Sections, s.Tags, s.Entries, s.Deprecated)
			return nil
		},
	}
}

func orNone(s string) string {
	if s == "" {
		return "(none, defaults)"
	}
	return s
}

func (a *app) sectionsCmd() *cobra.Command {
	return &cobra.Command{
		Use:     "sections",
		Aliases: []string{"sekce"},
		Short:   "List sections",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			st, err := a.open()
			if err != nil {
				return err
			}
			defer st.Close()
			ss, err := st.Sections(cmd.Context())
			if err != nil {
				return err
			}
			if a.asJSON {
				return a.printJSON(ss)
			}
			tw := newTable(a.out)
			fmt.Fprintln(tw, "ID\tNAME\tENTRIES\tGROUP")
			for _, s := range ss {
				fmt.Fprintf(tw, "%d\t%s\t%d\t%s\n", s.ID, s.Name, s.Entries, s.Label)
			}
			return tw.Flush()
		},
	}
}

func (a *app) tagsCmd() *cobra.Command {
	var sections []string
	var typ string
	cmd := &cobra.Command{
		Use:   "tags",
		Short: "List tags and books",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			st, err := a.open()
			if err != nil {
				return err
			}
			defer st.Close()
			ctx := cmd.Context()
			ids, err := store.ResolveSections(ctx, st, sections)
			if err != nil {
				return err
			}
			ts, err := st.Tags(ctx, model.TagFilter{SectionIDs: ids, Type: typ})
			if err != nil {
				return err
			}
			if a.asJSON {
				return a.printJSON(ts)
			}
			names := map[int]string{}
			ss, err := st.Sections(ctx)
			if err != nil {
				return err
			}
			for _, s := range ss {
				names[s.ID] = s.Name
			}
			tw := newTable(a.out)
			fmt.Fprintln(tw, "ID\tNAME\tTYPE\tENTRIES\tSECTION")
			for _, t := range ts {
				fmt.Fprintf(tw, "%d\t%s\t%s\t%d\t%s\n", t.ID, t.Name, t.Type, t.Entries, names[t.SectionID])
			}
			return tw.Flush()
		},
	}
	cmd.Flags().StringSliceVarP(&sections, "section", "s", nil, "only tags of these sections (name or id, repeatable)")
	cmd.Flags().StringVar(&typ, "type", "", "tag or book")
	return cmd
}

func (a *app) searchCmd() *cobra.Command {
	var q model.Query
	var mode, field string
	var exact, prefix, fts, en, cs bool
	var sections, tags []string
	cmd := &cobra.Command{
		Use:     "search [query...]",
		Aliases: []string{"s", "hledat"},
		Short:   "Search translation keys",
		Long: `Search translation keys. Matching ignores case and diacritics.

Modes: substring (default), --exact, --prefix, --fts (full-text syntax:
words, "phrases", OR, NOT, prefix*). By default both the English original and
the Czech translation are searched; use --en / --cs or --field.

Examples:
  pk search fireball
  pk search --exact "magic missile" -s "D&D 5e"
  pk search --cs ohnivá koule
  pk search --fts 'dragon NOT red' -n 50
  pk search -s "D&D 5e" -t KOUZLO -n 100     # all spells of a section`,
		RunE: func(cmd *cobra.Command, args []string) error {
			st, err := a.open()
			if err != nil {
				return err
			}
			defer st.Close()
			ctx := cmd.Context()
			q.Text = strings.Join(args, " ")
			q.Mode, q.Field = model.Mode(mode), model.Field(field)
			switch {
			case exact:
				q.Mode = model.ModeExact
			case prefix:
				q.Mode = model.ModePrefix
			case fts:
				q.Mode = model.ModeFTS
			}
			switch {
			case en:
				q.Field = model.FieldOriginal
			case cs:
				q.Field = model.FieldTranslation
			}
			if q.SectionIDs, err = store.ResolveSections(ctx, st, sections); err != nil {
				return err
			}
			if q.TagGroups, err = store.ResolveTags(ctx, st, tags, q.SectionIDs); err != nil {
				return err
			}
			res, err := st.Search(ctx, q)
			if err != nil {
				return err
			}
			if a.asJSON {
				return a.printJSON(res)
			}
			printResults(a.out, res)
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVarP(&mode, "mode", "m", "substring", "substring, exact, prefix or fts")
	f.BoolVarP(&exact, "exact", "e", false, "whole-string match (same as --mode exact)")
	f.BoolVarP(&prefix, "prefix", "p", false, "starts-with match")
	f.BoolVar(&fts, "fts", false, "full-text query syntax")
	f.StringVarP(&field, "field", "f", "both", "both, original, translation or all (incl. notes)")
	f.BoolVar(&en, "en", false, "search only the English original")
	f.BoolVar(&cs, "cs", false, "search only the Czech translation")
	f.StringSliceVarP(&sections, "section", "s", nil, "section name or id (repeatable; any of them)")
	f.StringSliceVarP(&tags, "tag", "t", nil, "tag or book name/id (repeatable; all of them)")
	f.BoolVarP(&q.IncludeDeprecated, "deprecated", "d", false, "include deprecated translations")
	f.IntVarP(&q.Limit, "limit", "n", 20, "max results")
	f.IntVar(&q.Offset, "offset", 0, "skip results")
	return cmd
}

func (a *app) showCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "show ID",
		Short: "Show one entry with notes, tags and deprecated translations",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.Atoi(strings.TrimPrefix(args[0], "#"))
			if err != nil {
				return fmt.Errorf("invalid id %q", args[0])
			}
			st, err := a.open()
			if err != nil {
				return err
			}
			defer st.Close()
			e, err := st.Get(cmd.Context(), id)
			if err != nil {
				return err
			}
			if a.asJSON {
				return a.printJSON(e)
			}
			printEntry(a.out, e)
			return nil
		},
	}
}

func (a *app) mcpCmd() *cobra.Command {
	var transport, listen string
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "Run an MCP server (stdio or Streamable HTTP)",
		Long: `Serves the keys to MCP clients (Claude Code, Claude Desktop, ...).

The transport, listen address, periodic refresh etc. come from the config file
(see "pk config --example"); flags override it.

  claude mcp add pk -- pk mcp                         # local, stdio
  pk mcp --config server.yaml --transport http        # hosted`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if transport != "" {
				a.cfg.MCP.Transport = transport
			}
			if listen != "" {
				a.cfg.MCP.Listen = listen
			}
			if err := a.cfg.Validate(); err != nil {
				return err
			}
			// stdout belongs to the protocol in stdio mode; log to stderr.
			log := slog.New(slog.NewTextHandler(a.errOut, nil))
			st, err := a.open()
			if err != nil {
				return err
			}
			defer st.Close()
			ctx := cmd.Context()
			runImport := func() {
				rep, err := importer.Run(ctx, st, backend.Source(a.cfg, false), importer.Options{LockPath: a.cfg.LockPath()})
				if err != nil {
					log.Error("import failed", "err", err)
					return
				}
				log.Info("import done", "upToDate", rep.UpToDate, "entries", rep.Entries, "changed", rep.ChangedSections)
			}
			if _, err := st.Status(ctx); errors.Is(err, store.ErrNoData) {
				if a.cfg.MCP.ImportOnStart {
					runImport()
				} else {
					log.Warn("no data yet: run `pk import` (or set mcp.import_on_start)")
				}
			}
			if d := a.cfg.MCP.RefreshInterval.Duration; d > 0 {
				go func() {
					t := time.NewTicker(d)
					defer t.Stop()
					for {
						select {
						case <-ctx.Done():
							return
						case <-t.C:
							runImport()
						}
					}
				}()
			}
			srv := mcpserver.New(st)
			if a.cfg.MCP.Transport == "http" {
				return mcpserver.ServeHTTP(ctx, srv, a.cfg.MCP.Listen, a.cfg.MCP.Path, log)
			}
			return mcpserver.ServeStdio(ctx, srv)
		},
	}
	cmd.Flags().StringVar(&transport, "transport", "", "stdio or http (overrides config)")
	cmd.Flags().StringVar(&listen, "listen", "", "http listen address (overrides config)")
	return cmd
}

func (a *app) configCmd() *cobra.Command {
	var example bool
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Print the effective configuration (or an example file)",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if example {
				fmt.Fprint(a.out, config.Example)
				return nil
			}
			c := *a.cfg
			if c.Postgres.DSN != "" {
				c.Postgres.DSN = "(set)"
			}
			if a.asJSON {
				return a.printJSON(c)
			}
			fmt.Fprintf(a.out, "# loaded from: %s\n", orNone(c.File))
			return yaml.NewEncoder(a.out).Encode(c)
		},
	}
	cmd.Flags().BoolVar(&example, "example", false, "print a commented example config file")
	return cmd
}

func (a *app) versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the version",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			fmt.Fprintln(a.out, "pk", version.Version, version.Commit)
			return nil
		},
	}
}
