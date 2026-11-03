// Package importer updates a store from the published JSON files. It only
// rewrites sections whose sha256 changed, verifies the result against the
// source and only then lets the store switch to it.
package importer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gofrs/flock"

	"github.com/your-github-user/prekladoveklice-mcp/internal/model"
	"github.com/your-github-user/prekladoveklice-mcp/internal/source"
	"github.com/your-github-user/prekladoveklice-mcp/internal/store"
)

// Options control an import run.
type Options struct {
	Rebuild     bool   // ignore the manifest and rebuild everything
	DryRun      bool   // fetch, build and verify, but do not activate
	AllowShrink bool   // accept losing more than MaxShrink of the entries
	LockPath    string // file lock preventing concurrent imports
	Log         func(format string, args ...any)
}

// MaxShrink is the largest fraction of entries an import may drop without
// AllowShrink; it protects against a broken or truncated upstream.
const MaxShrink = 0.2

// Report describes what an import did.
type Report struct {
	UpToDate        bool     `json:"upToDate"`
	Fresh           bool     `json:"fresh"`
	DryRun          bool     `json:"dryRun,omitempty"`
	MetaChanged     bool     `json:"metaChanged"`
	ChangedSections []int    `json:"changedSections"`
	RemovedSections []int    `json:"removedSections"`
	Sections        int      `json:"sections"`
	Entries         int      `json:"entries"`
	PreviousEntries int      `json:"previousEntries"`
	Downloaded      int      `json:"downloaded"`
	NotModified     int      `json:"notModified"`
	Warnings        []string `json:"warnings,omitempty"`
}

// Run performs an import.
func Run(ctx context.Context, st store.Store, src *source.Client, opts Options) (*Report, error) {
	logf := opts.Log
	if logf == nil {
		logf = func(string, ...any) {}
	}
	if opts.LockPath != "" {
		if err := os.MkdirAll(filepath.Dir(opts.LockPath), 0o755); err != nil {
			return nil, err
		}
		lock := flock.New(opts.LockPath)
		ok, err := lock.TryLock()
		if err != nil {
			return nil, fmt.Errorf("lock %s: %w", opts.LockPath, err)
		}
		if !ok {
			return nil, fmt.Errorf("another import is running (lock %s)", opts.LockPath)
		}
		defer lock.Unlock()
	}

	manifest, err := st.Manifest(ctx)
	if err != nil {
		return nil, fmt.Errorf("read manifest: %w", err)
	}
	rep := &Report{DryRun: opts.DryRun, ChangedSections: []int{}, RemovedSections: []int{}}
	if status, err := st.Status(ctx); err == nil {
		rep.PreviousEntries = status.Entries + status.Deprecated
	}

	// Fetch everything; unchanged files come back as 304 and are read from
	// the raw snapshot, so every section can be verified, not just changed ones.
	var files []*source.File
	fetch := func(key string) (*source.File, error) {
		f, err := src.Fetch(ctx, key, manifest[key].ETag)
		if err != nil {
			return nil, err
		}
		if f.FromRaw {
			rep.NotModified++
		} else {
			rep.Downloaded++
		}
		files = append(files, f)
		return f, nil
	}

	appFile, err := fetch(source.AppDataKey)
	if err != nil {
		return nil, err
	}
	app, err := source.ParseAppData(appFile.Body)
	if err != nil {
		return nil, err
	}
	rep.MetaChanged = manifest[source.AppDataKey].SHA256 != appFile.SHA256
	rep.Sections = len(app.Sections)

	want := store.Expectation{Sections: len(app.Sections), Tags: len(app.Tags), EntriesPerSection: map[int]int{}}
	parsed := map[int][]model.RawEntry{}
	inSource := map[string]bool{source.AppDataKey: true}
	for _, sec := range app.Sections {
		key := source.SectionKey(sec.ID)
		inSource[key] = true
		f, err := fetch(key)
		if err != nil {
			return nil, err
		}
		es, err := source.ParseSection(sec.ID, f.Body)
		if err != nil {
			return nil, err
		}
		parsed[sec.ID] = es
		want.EntriesPerSection[sec.ID] = store.CountRows(es)
		if want.ProbeTerm == "" && len(es) > 0 {
			want.ProbeTerm = strings.Fields(es[0].Original)[0]
		}
		if manifest[key].SHA256 != f.SHA256 {
			rep.ChangedSections = append(rep.ChangedSections, sec.ID)
		}
		logf("fetched %-28s %s", sec.Name, status(f, manifest[key]))
	}
	for key := range manifest {
		if !inSource[key] && strings.HasPrefix(key, "section:") {
			id, _ := strconv.Atoi(strings.TrimPrefix(key, "section:"))
			rep.RemovedSections = append(rep.RemovedSections, id)
		}
	}
	sort.Ints(rep.RemovedSections)
	rep.Entries = want.Total()

	if !opts.Rebuild && !rep.MetaChanged && len(rep.ChangedSections) == 0 && len(rep.RemovedSections) == 0 {
		rep.UpToDate = true
		return rep, nil
	}
	if !opts.AllowShrink && rep.PreviousEntries > 0 &&
		float64(rep.Entries) < float64(rep.PreviousEntries)*(1-MaxShrink) {
		return rep, fmt.Errorf("source has %d entries, local data has %d: refusing to drop more than %.0f%% (use --allow-shrink if this is expected)",
			rep.Entries, rep.PreviousEntries, MaxShrink*100)
	}

	imp, err := st.BeginImport(ctx, opts.Rebuild)
	if err != nil {
		return nil, fmt.Errorf("begin import: %w", err)
	}
	rep.Fresh = imp.Fresh()
	if err := apply(ctx, imp, app, parsed, rep, files); err != nil {
		imp.Abort()
		return rep, err
	}
	logf("verifying %d entries", rep.Entries)
	warnings, err := imp.Verify(ctx, want)
	rep.Warnings = warnings
	if err != nil {
		imp.Abort()
		return rep, fmt.Errorf("new data failed verification, live data left untouched: %w", err)
	}
	if opts.DryRun {
		imp.Abort()
		return rep, nil
	}
	if err := imp.Commit(ctx); err != nil {
		return rep, err
	}

	var fetched []*source.File
	for _, f := range files {
		if !f.FromRaw {
			fetched = append(fetched, f)
		}
	}
	if err := src.SaveRaw(fetched); err != nil {
		rep.Warnings = append(rep.Warnings, "saving raw snapshot: "+err.Error())
	}
	var removed []string
	for _, id := range rep.RemovedSections {
		removed = append(removed, source.SectionKey(id))
	}
	src.RemoveRaw(removed)
	return rep, nil
}

func apply(ctx context.Context, imp store.Import, app *model.AppData, parsed map[int][]model.RawEntry, rep *Report, files []*source.File) error {
	fresh := imp.Fresh()
	if fresh || rep.MetaChanged {
		if err := imp.ReplaceMeta(ctx, app); err != nil {
			return fmt.Errorf("sections/tags: %w", err)
		}
	}
	changed := map[int]bool{}
	for _, id := range rep.ChangedSections {
		changed[id] = true
	}
	for _, sec := range app.Sections {
		if fresh || changed[sec.ID] {
			if err := imp.ReplaceSection(ctx, sec.ID, parsed[sec.ID]); err != nil {
				return err
			}
		}
	}
	if !fresh {
		for _, id := range rep.RemovedSections {
			if err := imp.DeleteSection(ctx, id); err != nil {
				return err
			}
		}
	}
	now := time.Now().UTC().Format(time.RFC3339)
	m := make([]model.ManifestEntry, 0, len(files))
	for _, f := range files {
		m = append(m, model.ManifestEntry{Key: f.Key, URL: f.URL, SHA256: f.SHA256, ETag: f.ETag, Size: int64(len(f.Body)), ImportedAt: now})
	}
	return imp.SetManifest(ctx, m)
}

func status(f *source.File, prev model.ManifestEntry) string {
	switch {
	case prev.SHA256 == "":
		return "new"
	case prev.SHA256 != f.SHA256:
		return "changed"
	case f.FromRaw:
		return "unchanged (304)"
	default:
		return "unchanged"
	}
}
