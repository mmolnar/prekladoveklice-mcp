package sqlite

// schemaVersion is bumped whenever schema changes; an import into a database
// with another version rebuilds it from the source files instead of migrating.
const schemaVersion = "1"

const schema = `
CREATE TABLE meta (
	key   TEXT PRIMARY KEY,
	value TEXT NOT NULL
);
CREATE TABLE sections (
	id       INTEGER PRIMARY KEY,
	name     TEXT NOT NULL,
	label    TEXT NOT NULL,
	position INTEGER NOT NULL
);
CREATE TABLE tags (
	id         INTEGER PRIMARY KEY,
	name       TEXT NOT NULL,
	section_id INTEGER NOT NULL,
	type       TEXT NOT NULL,
	position   INTEGER NOT NULL
);
CREATE INDEX tags_section ON tags(section_id);

-- Current keys and their nested deprecated translations (parent_id set).
CREATE TABLE entries (
	id               INTEGER PRIMARY KEY,
	section_id       INTEGER NOT NULL,
	parent_id        INTEGER,
	original         TEXT NOT NULL,
	translation      TEXT NOT NULL,
	deprecated       INTEGER NOT NULL,
	notes            TEXT NOT NULL, -- JSON array of strings
	uses             TEXT NOT NULL, -- JSON array, kept verbatim
	original_norm    TEXT NOT NULL, -- textnorm.Fold(original)
	translation_norm TEXT NOT NULL,
	notes_norm       TEXT NOT NULL
);
CREATE INDEX entries_section ON entries(section_id);
CREATE INDEX entries_parent ON entries(parent_id);
CREATE INDEX entries_original_norm ON entries(original_norm);
CREATE INDEX entries_translation_norm ON entries(translation_norm);

CREATE TABLE entry_tags (
	entry_id INTEGER NOT NULL,
	tag_id   INTEGER NOT NULL,
	PRIMARY KEY (entry_id, tag_id)
) WITHOUT ROWID;
CREATE INDEX entry_tags_tag ON entry_tags(tag_id, entry_id);

CREATE VIRTUAL TABLE entries_fts USING fts5(
	original, translation, notes,
	content='entries', content_rowid='id',
	tokenize='unicode61 remove_diacritics 2'
);

CREATE TABLE manifest (
	key         TEXT PRIMARY KEY,
	url         TEXT NOT NULL,
	sha256      TEXT NOT NULL,
	etag        TEXT NOT NULL,
	size        INTEGER NOT NULL,
	imported_at TEXT NOT NULL
);
`
