// Package store defines the storage interface implemented by each backend
// (SQLite, PostgreSQL) and helpers shared by all of them.
package store

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/mmolnar/prekladoveklice-mcp/internal/model"
	"github.com/mmolnar/prekladoveklice-mcp/internal/textnorm"
)

var (
	// ErrNotFound is returned by Get for unknown ids.
	ErrNotFound = errors.New("not found")
	// ErrNoData is returned by read methods before the first import.
	ErrNoData = errors.New("no data yet: run `pk import` first")
)

// Store is a searchable copy of the translation keys.
type Store interface {
	Status(ctx context.Context) (*model.Status, error)
	Manifest(ctx context.Context) (map[string]model.ManifestEntry, error)
	Sections(ctx context.Context) ([]model.Section, error)
	Tags(ctx context.Context, f model.TagFilter) ([]model.Tag, error)
	Search(ctx context.Context, q model.Query) (*model.SearchResult, error)
	Get(ctx context.Context, id int) (*model.Entry, error)

	// BeginImport starts building a new version of the data next to the live
	// one. Readers keep seeing the old data until Commit. If fresh is true (or
	// the backend needs a rebuild, e.g. after a schema change) the new version
	// starts empty and Import.Fresh reports true.
	BeginImport(ctx context.Context, fresh bool) (Import, error)
	Close() error
}

// Import is an in-progress update. Call Verify, then Commit; or Abort.
type Import interface {
	Fresh() bool
	ReplaceMeta(ctx context.Context, a *model.AppData) error
	ReplaceSection(ctx context.Context, sectionID int, entries []model.RawEntry) error
	DeleteSection(ctx context.Context, sectionID int) error
	SetManifest(ctx context.Context, m []model.ManifestEntry) error
	// Verify checks the new version against what the source files contain.
	// It returns non-fatal warnings, or an error if the data must not go live.
	Verify(ctx context.Context, want Expectation) ([]string, error)
	// Commit atomically makes the verified new version live, keeping the
	// previous one as a backup.
	Commit(ctx context.Context) error
	Abort() error
}

// Expectation is what a verified import must contain.
type Expectation struct {
	Sections int
	Tags     int
	// EntriesPerSection counts rows per section, deprecated translations included.
	EntriesPerSection map[int]int
	// ProbeTerm is a word that must be findable via full-text search.
	ProbeTerm string
}

// Total returns the expected number of entry rows.
func (e Expectation) Total() int {
	n := 0
	for _, c := range e.EntriesPerSection {
		n += c
	}
	return n
}

// CountRows counts an entry plus its nested deprecated translations.
func CountRows(es []model.RawEntry) int {
	n := 0
	for _, e := range es {
		n += 1 + len(e.DeprecatedTranslations)
	}
	return n
}

// ResolveSections maps section ids or names (case/diacritics-insensitive,
// unique substrings allowed) to ids.
func ResolveSections(ctx context.Context, s Store, refs []string) ([]int, error) {
	if len(refs) == 0 {
		return nil, nil
	}
	all, err := s.Sections(ctx)
	if err != nil {
		return nil, err
	}
	var ids []int
	for _, ref := range refs {
		ref = strings.TrimSpace(ref)
		if id, err := strconv.Atoi(ref); err == nil {
			ids = append(ids, id)
			continue
		}
		f := textnorm.Fold(ref)
		// Best match level wins: exact name, word-start substring, any substring.
		var best []model.Section
		bestLevel := 0
		for _, sec := range all {
			if l := matchLevel(textnorm.Fold(sec.Name), f); l > bestLevel {
				best, bestLevel = []model.Section{sec}, l
			} else if l > 0 && l == bestLevel {
				best = append(best, sec)
			}
		}
		switch {
		case len(best) == 1:
			ids = append(ids, best[0].ID)
		case len(best) > 1:
			return nil, fmt.Errorf("section %q is ambiguous: %s", ref, sectionNames(best))
		default:
			return nil, fmt.Errorf("unknown section %q (see `pk sections`)", ref)
		}
	}
	return ids, nil
}

// matchLevel scores how well folded name matches folded ref: 3 equal,
// 2 ref starts a word of name, 1 ref is a substring, 0 no match.
func matchLevel(name, ref string) int {
	switch {
	case name == ref:
		return 3
	case strings.HasPrefix(name, ref) || strings.Contains(name, " "+ref):
		return 2
	case strings.Contains(name, ref):
		return 1
	}
	return 0
}

// ResolveTags maps each tag reference (id or name) to the set of matching tag
// ids, optionally limited to some sections. Names match case- and
// diacritics-insensitively; the same name exists in several sections.
func ResolveTags(ctx context.Context, s Store, refs []string, sectionIDs []int) ([][]int, error) {
	if len(refs) == 0 {
		return nil, nil
	}
	all, err := s.Tags(ctx, model.TagFilter{SectionIDs: sectionIDs})
	if err != nil {
		return nil, err
	}
	var groups [][]int
	for _, ref := range refs {
		ref = strings.TrimSpace(ref)
		if id, err := strconv.Atoi(ref); err == nil {
			groups = append(groups, []int{id})
			continue
		}
		// Same name in several sections means several ids; they all count.
		f := textnorm.Fold(ref)
		byName := map[string][]int{}
		bestLevel := 0
		for _, t := range all {
			n := textnorm.Fold(t.Name)
			l := matchLevel(n, f)
			if l == 0 || l < bestLevel {
				continue
			}
			if l > bestLevel {
				byName, bestLevel = map[string][]int{}, l
			}
			byName[n] = append(byName[n], t.ID)
		}
		switch len(byName) {
		case 0:
			where := ""
			if len(sectionIDs) > 0 {
				where = " in the selected sections"
			}
			return nil, fmt.Errorf("unknown tag %q%s (see `pk tags`)", ref, where)
		case 1:
			for _, ids := range byName {
				groups = append(groups, ids)
			}
		default:
			var names []string
			for n := range byName {
				names = append(names, n)
			}
			sort.Strings(names)
			return nil, fmt.Errorf("tag %q is ambiguous: %s", ref, strings.Join(names, ", "))
		}
	}
	return groups, nil
}

func sectionNames(ss []model.Section) string {
	names := make([]string, len(ss))
	for i, s := range ss {
		names[i] = s.Name
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// Normalize fills defaults and clamps a query.
func Normalize(q *model.Query, maxLimit int) {
	if q.Mode == "" {
		q.Mode = model.ModeSubstring
	}
	if q.Field == "" {
		q.Field = model.FieldBoth
	}
	if q.Limit <= 0 {
		q.Limit = 20
	}
	if q.Limit > maxLimit {
		q.Limit = maxLimit
	}
	if q.Offset < 0 {
		q.Offset = 0
	}
	q.Text = strings.TrimSpace(q.Text)
}

// ValidateQuery rejects unknown modes and fields.
func ValidateQuery(q model.Query) error {
	switch q.Mode {
	case model.ModeSubstring, model.ModeExact, model.ModePrefix, model.ModeFTS:
	default:
		return fmt.Errorf("unknown mode %q (substring, exact, prefix, fts)", q.Mode)
	}
	switch q.Field {
	case model.FieldBoth, model.FieldOriginal, model.FieldTranslation, model.FieldAll:
	default:
		return fmt.Errorf("unknown field %q (both, original, translation, all)", q.Field)
	}
	if q.Mode == model.ModeFTS && q.Text == "" {
		return errors.New("fts mode needs a query")
	}
	return nil
}
