// Package model holds the data types shared by the fetcher, the stores and
// the user-facing commands.
package model

// Source JSON shapes, as published by prekladoveklice.d20.cz.

// AppData is the content of appData.json.
type AppData struct {
	Sections []Section `json:"sections"`
	Tags     []Tag     `json:"tags"`
}

// Section is one dictionary (e.g. "Bytosti", "D&D 5e").
type Section struct {
	ID    int    `json:"id"`
	Name  string `json:"name"`
	Label string `json:"label"` // group the section belongs to on the website
	// Entries is filled in by the stores (count of current, non-deprecated keys).
	Entries int `json:"entries,omitempty"`
}

// Tag is either a topical tag ("KOUZLO") or a source book; it belongs to a section.
type Tag struct {
	ID        int    `json:"id"`
	Name      string `json:"name"`
	SectionID int    `json:"sectionId"`
	Type      string `json:"type"` // "tag" or "book"
	// Entries is filled in by the stores.
	Entries int `json:"entries,omitempty"`
}

// RawEntry is one element of sekce/<id>.json.
type RawEntry struct {
	ID                     int        `json:"id"`
	Original               string     `json:"original"`
	Translation            string     `json:"translation"`
	Section                int        `json:"section"`
	Deprecated             bool       `json:"deprecated"`
	Notes                  []string   `json:"notes"`
	Uses                   []any      `json:"uses"`
	Tags                   []int      `json:"tags"`
	DeprecatedTranslations []RawEntry `json:"deprecatedTranslations"`
}

// Query-side types.

// TagRef is a tag as attached to an entry in results.
type TagRef struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
}

// Entry is a translation key as returned by searches.
type Entry struct {
	ID          int      `json:"id"`
	Original    string   `json:"original"`
	Translation string   `json:"translation"`
	SectionID   int      `json:"sectionId"`
	Section     string   `json:"section"`
	Deprecated  bool     `json:"deprecated,omitempty"`
	Notes       []string `json:"notes,omitempty"`
	Tags        []TagRef `json:"tags,omitempty"`
	// ReplacedBy is set on deprecated translations: the id of the current key.
	ReplacedBy int `json:"replacedBy,omitempty"`
	// DeprecatedTranslations lists older translations of this key (only filled by Get).
	DeprecatedTranslations []DeprecatedTranslation `json:"deprecatedTranslations,omitempty"`
}

// DeprecatedTranslation is a superseded translation of an entry. (A separate,
// non-recursive type so tool output schemas can be generated.)
type DeprecatedTranslation struct {
	ID          int      `json:"id"`
	Original    string   `json:"original"`
	Translation string   `json:"translation"`
	Notes       []string `json:"notes,omitempty"`
}

// Mode selects how Query.Text is matched.
type Mode string

const (
	ModeSubstring Mode = "substring"
	ModeExact     Mode = "exact"
	ModePrefix    Mode = "prefix"
	ModeFTS       Mode = "fts" // full-text query syntax (words, "phrases", OR, prefix*)
)

// Field selects which columns Query.Text is matched against.
type Field string

const (
	FieldBoth        Field = "both" // original + translation
	FieldOriginal    Field = "original"
	FieldTranslation Field = "translation"
	FieldAll         Field = "all" // original + translation + notes
)

// Query describes a search. Text matching ignores case and diacritics.
type Query struct {
	Text  string
	Mode  Mode
	Field Field
	// SectionIDs restricts results to any of these sections.
	SectionIDs []int
	// TagGroups: an entry must have at least one tag from every group.
	// (A tag name can resolve to several ids, one per section.)
	TagGroups         [][]int
	IncludeDeprecated bool
	Limit             int
	Offset            int
}

// SearchResult is a page of results plus the total number of matches.
type SearchResult struct {
	Total   int     `json:"total"`
	Offset  int     `json:"offset"`
	Entries []Entry `json:"entries"`
}

// TagFilter narrows ListTags.
type TagFilter struct {
	SectionIDs []int
	Type       string // "", "tag" or "book"
}

// ManifestEntry records the source file a store was last imported from.
type ManifestEntry struct {
	Key        string `json:"key"` // "appData" or "section:<id>"
	URL        string `json:"url"`
	SHA256     string `json:"sha256"`
	ETag       string `json:"etag,omitempty"`
	Size       int64  `json:"size"`
	ImportedAt string `json:"importedAt"`
}

// Status summarises a store.
type Status struct {
	Backend      string `json:"backend"`
	Location     string `json:"location"`
	Sections     int    `json:"sections"`
	Tags         int    `json:"tags"`
	Entries      int    `json:"entries"`
	Deprecated   int    `json:"deprecated"`
	LastImport   string `json:"lastImport,omitempty"`
	SourceSHA256 string `json:"sourceSha256,omitempty"` // of appData.json
}
