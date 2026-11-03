package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"time"

	"github.com/your-github-user/prekladoveklice-mcp/internal/model"
	"github.com/your-github-user/prekladoveklice-mcp/internal/store"
	"github.com/your-github-user/prekladoveklice-mcp/internal/textnorm"
)

// importer builds path.new from a copy of the live database (or from
// scratch), and on Commit keeps the live file as path.bak and renames
// path.new over path.
type importer struct {
	s        *Store
	newPath  string
	db       *sql.DB
	tx       *sql.Tx
	fresh    bool
	verified bool
}

func (s *Store) BeginImport(ctx context.Context, fresh bool) (store.Import, error) {
	imp := &importer{s: s, newPath: s.path + ".new", fresh: fresh}
	removeDB(imp.newPath)

	if !fresh {
		live, err := s.conn()
		switch {
		case errors.Is(err, store.ErrNoData):
			imp.fresh = true
		case err != nil:
			// Unreadable live file: rebuild instead of copying it.
			imp.fresh = true
		default:
			var v string
			_ = live.QueryRowContext(ctx, `SELECT value FROM meta WHERE key = 'schema_version'`).Scan(&v)
			if v != schemaVersion {
				imp.fresh = true
			} else if _, err := live.ExecContext(ctx, `VACUUM INTO ?`, imp.newPath); err != nil {
				return nil, fmt.Errorf("copy live database: %w", err)
			}
		}
	}

	db, err := sql.Open("sqlite", dsn(imp.newPath, false))
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	imp.db = db
	if imp.fresh {
		if _, err := db.ExecContext(ctx, schema); err != nil {
			imp.Abort()
			return nil, fmt.Errorf("create schema: %w", err)
		}
	}
	if imp.tx, err = db.BeginTx(ctx, nil); err != nil {
		imp.Abort()
		return nil, err
	}
	return imp, nil
}

func (i *importer) Fresh() bool { return i.fresh }

func (i *importer) ReplaceMeta(ctx context.Context, a *model.AppData) error {
	for _, q := range []string{`DELETE FROM sections`, `DELETE FROM tags`} {
		if _, err := i.tx.ExecContext(ctx, q); err != nil {
			return err
		}
	}
	for pos, s := range a.Sections {
		if _, err := i.tx.ExecContext(ctx, `INSERT INTO sections (id, name, label, position) VALUES (?, ?, ?, ?)`,
			s.ID, s.Name, s.Label, pos); err != nil {
			return fmt.Errorf("section %d: %w", s.ID, err)
		}
	}
	for pos, t := range a.Tags {
		if _, err := i.tx.ExecContext(ctx, `INSERT INTO tags (id, name, section_id, type, position) VALUES (?, ?, ?, ?, ?)`,
			t.ID, t.Name, t.SectionID, t.Type, pos); err != nil {
			return fmt.Errorf("tag %d: %w", t.ID, err)
		}
	}
	return nil
}

func (i *importer) DeleteSection(ctx context.Context, id int) error {
	if _, err := i.tx.ExecContext(ctx, `DELETE FROM entry_tags WHERE entry_id IN (SELECT id FROM entries WHERE section_id = ?)`, id); err != nil {
		return err
	}
	_, err := i.tx.ExecContext(ctx, `DELETE FROM entries WHERE section_id = ?`, id)
	return err
}

func (i *importer) ReplaceSection(ctx context.Context, id int, es []model.RawEntry) error {
	if err := i.DeleteSection(ctx, id); err != nil {
		return err
	}
	ins, err := i.tx.PrepareContext(ctx, `INSERT INTO entries
		(id, section_id, parent_id, original, translation, deprecated, notes, uses, original_norm, translation_norm, notes_norm)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer ins.Close()
	insTag, err := i.tx.PrepareContext(ctx, `INSERT OR IGNORE INTO entry_tags (entry_id, tag_id) VALUES (?, ?)`)
	if err != nil {
		return err
	}
	defer insTag.Close()

	var add func(e model.RawEntry, parent any, deprecated bool) error
	add = func(e model.RawEntry, parent any, deprecated bool) error {
		notes := e.Notes
		if notes == nil {
			notes = []string{}
		}
		uses := e.Uses
		if uses == nil {
			uses = []any{}
		}
		nj, _ := json.Marshal(notes)
		uj, _ := json.Marshal(uses)
		noteText := ""
		for _, n := range notes {
			noteText += n + " "
		}
		if _, err := ins.ExecContext(ctx, e.ID, id, parent, e.Original, e.Translation, deprecated,
			string(nj), string(uj), textnorm.Fold(e.Original), textnorm.Fold(e.Translation), textnorm.Fold(noteText)); err != nil {
			return fmt.Errorf("section %d entry %d: %w", id, e.ID, err)
		}
		for _, t := range e.Tags {
			if _, err := insTag.ExecContext(ctx, e.ID, t); err != nil {
				return err
			}
		}
		return nil
	}
	for _, e := range es {
		if err := add(e, nil, e.Deprecated); err != nil {
			return err
		}
		for _, d := range e.DeprecatedTranslations {
			if err := add(d, e.ID, true); err != nil {
				return err
			}
		}
	}
	return nil
}

func (i *importer) SetManifest(ctx context.Context, m []model.ManifestEntry) error {
	if _, err := i.tx.ExecContext(ctx, `DELETE FROM manifest`); err != nil {
		return err
	}
	for _, e := range m {
		if _, err := i.tx.ExecContext(ctx, `INSERT INTO manifest (key, url, sha256, etag, size, imported_at) VALUES (?, ?, ?, ?, ?, ?)`,
			e.Key, e.URL, e.SHA256, e.ETag, e.Size, e.ImportedAt); err != nil {
			return err
		}
	}
	return nil
}

func (i *importer) Verify(ctx context.Context, want store.Expectation) ([]string, error) {
	var warnings []string
	tx := i.tx
	if _, err := tx.ExecContext(ctx, `INSERT INTO entries_fts(entries_fts) VALUES ('rebuild')`); err != nil {
		return nil, fmt.Errorf("build full-text index: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO meta (key, value) VALUES ('schema_version', ?1), ('last_import', ?2)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value`,
		schemaVersion, time.Now().UTC().Format(time.RFC3339)); err != nil {
		return nil, err
	}

	var nSections, nTags int
	if err := tx.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM sections), (SELECT count(*) FROM tags)`).Scan(&nSections, &nTags); err != nil {
		return nil, err
	}
	if nSections != want.Sections || nTags != want.Tags {
		return nil, fmt.Errorf("verify: have %d sections / %d tags, source has %d / %d", nSections, nTags, want.Sections, want.Tags)
	}
	have := map[int]int{}
	rows, err := tx.QueryContext(ctx, `SELECT section_id, count(*) FROM entries GROUP BY section_id`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id, n int
		if err := rows.Scan(&id, &n); err != nil {
			rows.Close()
			return nil, err
		}
		have[id] = n
	}
	rows.Close()
	for id, n := range want.EntriesPerSection {
		if have[id] != n {
			return nil, fmt.Errorf("verify: section %d has %d entries, source has %d", id, have[id], n)
		}
		delete(have, id)
	}
	for id, n := range have {
		return nil, fmt.Errorf("verify: %d entries of section %d which is not in the source", n, id)
	}
	var nFTS, orphanTags int
	if err := tx.QueryRowContext(ctx, `SELECT
		(SELECT count(*) FROM entries_fts),
		(SELECT count(*) FROM entry_tags WHERE tag_id NOT IN (SELECT id FROM tags))`).Scan(&nFTS, &orphanTags); err != nil {
		return nil, err
	}
	if nFTS != want.Total() {
		return nil, fmt.Errorf("verify: full-text index has %d rows, want %d", nFTS, want.Total())
	}
	if orphanTags > 0 {
		warnings = append(warnings, fmt.Sprintf("%d tag references point to tags missing from appData.json", orphanTags))
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	i.tx = nil

	// Checks on the committed file, as readers will see it.
	var ok string
	if err := i.db.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&ok); err != nil || ok != "ok" {
		return nil, fmt.Errorf("verify: integrity_check: %q %v", ok, err)
	}
	if _, err := i.db.ExecContext(ctx, `INSERT INTO entries_fts(entries_fts, rank) VALUES ('integrity-check', 1)`); err != nil {
		return nil, fmt.Errorf("verify: full-text index integrity: %w", err)
	}
	if want.ProbeTerm != "" {
		var n int
		if err := i.db.QueryRowContext(ctx, `SELECT count(*) FROM entries_fts WHERE entries_fts MATCH ?`,
			`"`+want.ProbeTerm+`"`).Scan(&n); err != nil || n == 0 {
			return nil, fmt.Errorf("verify: probe search for %q found nothing (%v)", want.ProbeTerm, err)
		}
	}
	if _, err := i.db.ExecContext(ctx, `PRAGMA optimize`); err != nil {
		warnings = append(warnings, "optimize: "+err.Error())
	}
	i.verified = true
	return warnings, nil
}

func (i *importer) Commit(ctx context.Context) error {
	if !i.verified {
		i.Abort()
		return errors.New("commit before successful verify")
	}
	if err := i.db.Close(); err != nil {
		return err
	}
	i.db = nil
	live, bak := i.s.path, i.s.path+".bak"
	if _, err := os.Stat(live); err == nil {
		removeDB(bak)
		// Hard-link keeps the live file in place until the atomic rename below.
		if err := os.Link(live, bak); err != nil {
			if err := copyFile(live, bak); err != nil {
				return fmt.Errorf("backup %s: %w", live, err)
			}
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err := os.Rename(i.newPath, live); err != nil {
		return fmt.Errorf("activate new database: %w", err)
	}
	return nil
}

func (i *importer) Abort() error {
	if i.tx != nil {
		i.tx.Rollback()
		i.tx = nil
	}
	if i.db != nil {
		i.db.Close()
		i.db = nil
	}
	removeDB(i.newPath)
	return nil
}

func removeDB(path string) {
	for _, suffix := range []string{"", "-journal", "-wal", "-shm"} {
		_ = os.Remove(path + suffix)
	}
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
