// Package sqlite is the single-file SQLite backend (pure Go, no cgo).
package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"strings"
	"sync"

	_ "modernc.org/sqlite"

	"github.com/mmolnar/prekladoveklice-mcp/internal/model"
	"github.com/mmolnar/prekladoveklice-mcp/internal/store"
	"github.com/mmolnar/prekladoveklice-mcp/internal/textnorm"
)

const maxLimit = 500

// Store reads the live database file. Imports build path+".new", verify it
// and rename it over path; Store notices the new file and reopens.
type Store struct {
	path string

	mu   sync.Mutex
	db   *sql.DB
	info fs.FileInfo // of the file db was opened from
}

var _ store.Store = (*Store)(nil)

// Open returns a store for the database at path. The file may not exist yet.
func Open(path string) (*Store, error) {
	s := &Store{path: path}
	if _, err := s.conn(); err != nil && !errors.Is(err, store.ErrNoData) {
		return nil, err
	}
	return s, nil
}

func dsn(path string, readOnly bool) string {
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(5000)")
	if readOnly {
		q.Add("mode", "ro")
	}
	return "file:" + path + "?" + q.Encode()
}

// conn returns a read-only handle on the current live file, reopening it if
// an import has replaced the file since it was opened.
func (s *Store) conn() (*sql.DB, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	fi, err := os.Stat(s.path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, store.ErrNoData
	}
	if err != nil {
		return nil, err
	}
	if s.db != nil && os.SameFile(fi, s.info) && fi.ModTime().Equal(s.info.ModTime()) {
		return s.db, nil
	}
	if s.db != nil {
		s.db.Close()
		s.db = nil
	}
	db, err := sql.Open("sqlite", dsn(s.path, true))
	if err != nil {
		return nil, err
	}
	var v string
	if err := db.QueryRow(`SELECT value FROM meta WHERE key = 'schema_version'`).Scan(&v); err != nil {
		db.Close()
		return nil, fmt.Errorf("%s is not a pk database (%v); run `pk import --rebuild`", s.path, err)
	}
	s.db, s.info = db, fi
	return db, nil
}

func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db != nil {
		return s.db.Close()
	}
	return nil
}

func (s *Store) Status(ctx context.Context) (*model.Status, error) {
	st := &model.Status{Backend: "sqlite", Location: s.path}
	db, err := s.conn()
	if err != nil {
		return st, err
	}
	err = db.QueryRowContext(ctx, `SELECT
		(SELECT count(*) FROM sections),
		(SELECT count(*) FROM tags),
		(SELECT count(*) FROM entries WHERE deprecated = 0),
		(SELECT count(*) FROM entries WHERE deprecated = 1),
		COALESCE((SELECT value FROM meta WHERE key = 'last_import'), ''),
		COALESCE((SELECT sha256 FROM manifest WHERE key = 'appData'), '')`).
		Scan(&st.Sections, &st.Tags, &st.Entries, &st.Deprecated, &st.LastImport, &st.SourceSHA256)
	return st, err
}

func (s *Store) Manifest(ctx context.Context) (map[string]model.ManifestEntry, error) {
	m := map[string]model.ManifestEntry{}
	db, err := s.conn()
	if errors.Is(err, store.ErrNoData) {
		return m, nil
	}
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT key, url, sha256, etag, size, imported_at FROM manifest`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var e model.ManifestEntry
		if err := rows.Scan(&e.Key, &e.URL, &e.SHA256, &e.ETag, &e.Size, &e.ImportedAt); err != nil {
			return nil, err
		}
		m[e.Key] = e
	}
	return m, rows.Err()
}

func (s *Store) Sections(ctx context.Context) ([]model.Section, error) {
	db, err := s.conn()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT s.id, s.name, s.label,
		(SELECT count(*) FROM entries e WHERE e.section_id = s.id AND e.deprecated = 0)
		FROM sections s ORDER BY s.position`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Section
	for rows.Next() {
		var sec model.Section
		if err := rows.Scan(&sec.ID, &sec.Name, &sec.Label, &sec.Entries); err != nil {
			return nil, err
		}
		out = append(out, sec)
	}
	return out, rows.Err()
}

func (s *Store) Tags(ctx context.Context, f model.TagFilter) ([]model.Tag, error) {
	db, err := s.conn()
	if err != nil {
		return nil, err
	}
	var where []string
	var args []any
	if len(f.SectionIDs) > 0 {
		where = append(where, "t.section_id IN ("+placeholders(len(f.SectionIDs))+")")
		args = appendInts(args, f.SectionIDs)
	}
	if f.Type != "" {
		where = append(where, "t.type = ?")
		args = append(args, f.Type)
	}
	q := `SELECT t.id, t.name, t.section_id, t.type,
		(SELECT count(*) FROM entry_tags et WHERE et.tag_id = t.id)
		FROM tags t JOIN sections s ON s.id = t.section_id`
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY s.position, t.type, t.position"
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Tag
	for rows.Next() {
		var t model.Tag
		if err := rows.Scan(&t.ID, &t.Name, &t.SectionID, &t.Type, &t.Entries); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

const entryCols = `e.id, e.original, e.translation, e.section_id, s.name, e.deprecated, e.notes, COALESCE(e.parent_id, 0)`

func (s *Store) Search(ctx context.Context, q model.Query) (*model.SearchResult, error) {
	store.Normalize(&q, maxLimit)
	if err := store.ValidateQuery(q); err != nil {
		return nil, err
	}
	db, err := s.conn()
	if err != nil {
		return nil, err
	}

	from := "entries e JOIN sections s ON s.id = e.section_id"
	var where []string
	var args, rankArgs []any
	order := "s.position, e.original_norm"

	if !q.IncludeDeprecated {
		where = append(where, "e.deprecated = 0")
	}
	if len(q.SectionIDs) > 0 {
		where = append(where, "e.section_id IN ("+placeholders(len(q.SectionIDs))+")")
		args = appendInts(args, q.SectionIDs)
	}
	for _, g := range q.TagGroups {
		// Deprecated translations inherit the tags of their current key.
		where = append(where, "EXISTS (SELECT 1 FROM entry_tags et WHERE et.entry_id = COALESCE(e.parent_id, e.id) AND et.tag_id IN ("+placeholders(len(g))+"))")
		args = appendInts(args, g)
	}

	if q.Text != "" {
		if q.Mode == model.ModeFTS {
			from += " JOIN entries_fts ON entries_fts.rowid = e.id"
			where = append(where, "entries_fts MATCH ?")
			args = append(args, ftsColumnFilter(q.Field)+"("+q.Text+")")
			order = "bm25(entries_fts), length(e.original)"
		} else {
			cols := normCols(q.Field)
			t := textnorm.Fold(q.Text)
			var conds, ranks []string
			for _, c := range cols {
				switch q.Mode {
				case model.ModeExact:
					conds = append(conds, c+" = ?")
					args = append(args, t)
				case model.ModePrefix:
					conds = append(conds, c+` LIKE ? ESCAPE '\'`)
					args = append(args, escapeLike(t)+"%")
				default:
					conds = append(conds, c+` LIKE ? ESCAPE '\'`)
					args = append(args, "%"+escapeLike(t)+"%")
				}
				// Rank: exact match, then prefix, then word start, then anywhere.
				ranks = append(ranks, "CASE WHEN "+c+" = ? THEN 0 WHEN "+c+` LIKE ? ESCAPE '\' THEN 1 WHEN `+c+` LIKE ? ESCAPE '\' THEN 2 ELSE 3 END`)
				rankArgs = append(rankArgs, t, escapeLike(t)+"%", "% "+escapeLike(t)+"%")
			}
			where = append(where, "("+strings.Join(conds, " OR ")+")")
			rank := ranks[0]
			if len(ranks) > 1 {
				rank = "min(" + strings.Join(ranks, ", ") + ")"
			}
			order = rank + ", length(e.original), e.original_norm"
		}
	}

	w := ""
	if len(where) > 0 {
		w = " WHERE " + strings.Join(where, " AND ")
	}
	res := &model.SearchResult{Offset: q.Offset, Entries: []model.Entry{}}
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM "+from+w, args...).Scan(&res.Total); err != nil {
		return nil, queryErr(q, err)
	}
	pageArgs := append(append(append([]any{}, args...), rankArgs...), q.Limit, q.Offset)
	rows, err := db.QueryContext(ctx, "SELECT "+entryCols+" FROM "+from+w+" ORDER BY "+order+" LIMIT ? OFFSET ?", pageArgs...)
	if err != nil {
		return nil, queryErr(q, err)
	}
	res.Entries, err = scanEntries(rows)
	if err != nil {
		return nil, err
	}
	if err := attachTags(ctx, db, res.Entries); err != nil {
		return nil, err
	}
	return res, nil
}

func (s *Store) Get(ctx context.Context, id int) (*model.Entry, error) {
	db, err := s.conn()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, "SELECT "+entryCols+` FROM entries e JOIN sections s ON s.id = e.section_id
		WHERE e.id = ? OR e.parent_id = ? ORDER BY e.parent_id IS NOT NULL, e.id`, id, id)
	if err != nil {
		return nil, err
	}
	es, err := scanEntries(rows)
	if err != nil {
		return nil, err
	}
	if len(es) == 0 || es[0].ID != id {
		return nil, store.ErrNotFound
	}
	if err := attachTags(ctx, db, es); err != nil {
		return nil, err
	}
	e := es[0]
	for _, d := range es[1:] {
		e.DeprecatedTranslations = append(e.DeprecatedTranslations,
			model.DeprecatedTranslation{ID: d.ID, Original: d.Original, Translation: d.Translation, Notes: d.Notes})
	}
	return &e, nil
}

func scanEntries(rows *sql.Rows) ([]model.Entry, error) {
	defer rows.Close()
	out := []model.Entry{}
	for rows.Next() {
		var e model.Entry
		var notes string
		if err := rows.Scan(&e.ID, &e.Original, &e.Translation, &e.SectionID, &e.Section, &e.Deprecated, &notes, &e.ReplacedBy); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(notes), &e.Notes); err != nil {
			return nil, fmt.Errorf("entry %d notes: %w", e.ID, err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func attachTags(ctx context.Context, db *sql.DB, es []model.Entry) error {
	if len(es) == 0 {
		return nil
	}
	idx := map[int][]int{}
	ids := make([]int, 0, len(es))
	for i, e := range es {
		idx[e.ID] = append(idx[e.ID], i)
		ids = append(ids, e.ID)
	}
	rows, err := db.QueryContext(ctx, `SELECT et.entry_id, t.id, t.name, t.type FROM entry_tags et
		JOIN tags t ON t.id = et.tag_id WHERE et.entry_id IN (`+placeholders(len(ids))+`)
		ORDER BY t.type DESC, t.position`, appendInts(nil, ids)...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var eid int
		var t model.TagRef
		if err := rows.Scan(&eid, &t.ID, &t.Name, &t.Type); err != nil {
			return err
		}
		for _, i := range idx[eid] {
			es[i].Tags = append(es[i].Tags, t)
		}
	}
	return rows.Err()
}

func normCols(f model.Field) []string {
	switch f {
	case model.FieldOriginal:
		return []string{"e.original_norm"}
	case model.FieldTranslation:
		return []string{"e.translation_norm"}
	case model.FieldAll:
		return []string{"e.original_norm", "e.translation_norm", "e.notes_norm"}
	default:
		return []string{"e.original_norm", "e.translation_norm"}
	}
}

func ftsColumnFilter(f model.Field) string {
	switch f {
	case model.FieldOriginal:
		return "original : "
	case model.FieldTranslation:
		return "translation : "
	case model.FieldAll:
		return ""
	default:
		return "{original translation} : "
	}
}

func queryErr(q model.Query, err error) error {
	if q.Mode == model.ModeFTS {
		return fmt.Errorf("invalid full-text query %q: %v (quote phrases, use OR/AND/NOT, prefix*)", q.Text, err)
	}
	return err
}

func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

func placeholders(n int) string {
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

func appendInts(args []any, ids []int) []any {
	for _, id := range ids {
		args = append(args, id)
	}
	return args
}
