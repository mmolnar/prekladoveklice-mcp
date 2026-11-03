package importer_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/mmolnar/prekladoveklice-mcp/internal/importer"
	"github.com/mmolnar/prekladoveklice-mcp/internal/model"
	"github.com/mmolnar/prekladoveklice-mcp/internal/source"
	"github.com/mmolnar/prekladoveklice-mcp/internal/store"
	"github.com/mmolnar/prekladoveklice-mcp/internal/store/sqlite"
)

// upstream is a fake prekladoveklice site whose files tests can change.
type upstream struct {
	mu    sync.Mutex
	files map[string][]byte // URL path -> body
	hits  map[string]int
}

func (u *upstream) set(path string, v any) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if b, ok := v.([]byte); ok {
		u.files[path] = b
		return
	}
	b, _ := json.Marshal(v)
	u.files[path] = b
}

func (u *upstream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	u.mu.Lock()
	body, ok := u.files[r.URL.Path]
	u.hits[r.URL.Path]++
	u.mu.Unlock()
	if !ok {
		http.NotFound(w, r)
		return
	}
	etag := `"` + source.Sum(body)[:16] + `"`
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("ETag", etag)
	w.Write(body)
}

func appData(sectionIDs ...int) model.AppData {
	a := model.AppData{}
	for _, id := range sectionIDs {
		a.Sections = append(a.Sections, model.Section{ID: id, Name: map[int]string{5: "Bytosti", 167: "D&D 5e", 3: "D&D 3.5e"}[id], Label: "test"})
	}
	a.Tags = []model.Tag{
		{ID: 121, Name: "TVOR", SectionID: 5, Type: "tag"},
		{ID: 247, Name: "Příručka hráče", SectionID: 167, Type: "book"},
		{ID: 248, Name: "Bestiář", SectionID: 167, Type: "book"},
	}
	return a
}

func bytosti() []model.RawEntry {
	return []model.RawEntry{
		{ID: 1, Original: "aberration", Translation: "zrůda", Section: 5, Tags: []int{121}},
		{ID: 2, Original: "air elemental", Translation: "vzdušný elementál", Section: 5, Tags: []int{121}},
		{ID: 3, Original: "dragon", Translation: "drak", Section: 5, Tags: []int{121}, Notes: []string{"(mužský rod)"}},
	}
}

func dnd5e() []model.RawEntry {
	return []model.RawEntry{
		{ID: 10, Original: "Fireball", Translation: "ohnivá koule", Section: 167, Tags: []int{247}, Notes: []string{"kouzlo"}},
		{ID: 11, Original: "Magic Missile", Translation: "magická střela", Section: 167, Tags: []int{247}},
		{ID: 12, Original: "Melf’s Minute Meteors", Translation: "Melfovy meteorky", Section: 167, Tags: []int{247},
			DeprecatedTranslations: []model.RawEntry{{ID: 13, Original: "Melf’s Minute Meteors", Translation: "Melfovy drobné meteory", Section: 167, Deprecated: true}}},
		{ID: 14, Original: "Red Dragon", Translation: "červený drak", Section: 167, Tags: []int{248}},
		{ID: 15, Original: "Dragon", Translation: "drak", Section: 167, Tags: []int{248}},
	}
}

type env struct {
	t   *testing.T
	up  *upstream
	srv *httptest.Server
	dir string
	st  *sqlite.Store
	src *source.Client
}

func newEnv(t *testing.T) *env {
	up := &upstream{files: map[string][]byte{}, hits: map[string]int{}}
	up.set("/pk/appData.json", appData(5, 167))
	up.set("/pk/sekce/5.json", bytosti())
	up.set("/pk/sekce/167.json", dnd5e())
	srv := httptest.NewServer(up)
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	st, err := sqlite.Open(filepath.Join(dir, "keys.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return &env{t: t, up: up, srv: srv, dir: dir, st: st,
		src: &source.Client{BaseURL: srv.URL + "/pk", RawDir: filepath.Join(dir, "raw"), UserAgent: "test"}}
}

func (e *env) run(opts importer.Options) (*importer.Report, error) {
	opts.LockPath = filepath.Join(e.dir, "import.lock")
	return importer.Run(context.Background(), e.st, e.src, opts)
}

func (e *env) mustRun(opts importer.Options) *importer.Report {
	e.t.Helper()
	r, err := e.run(opts)
	if err != nil {
		e.t.Fatalf("import: %v", err)
	}
	return r
}

func (e *env) search(q model.Query) *model.SearchResult {
	e.t.Helper()
	r, err := e.st.Search(context.Background(), q)
	if err != nil {
		e.t.Fatalf("search %+v: %v", q, err)
	}
	return r
}

func (e *env) noLeftovers() {
	e.t.Helper()
	if _, err := os.Stat(filepath.Join(e.dir, "keys.db.new")); err == nil {
		e.t.Error("keys.db.new left behind")
	}
}

func TestImportIncremental(t *testing.T) {
	e := newEnv(t)
	r := e.mustRun(importer.Options{})
	if !r.Fresh || r.Entries != 9 || len(r.ChangedSections) != 2 {
		t.Fatalf("first import: %+v", r)
	}

	r = e.mustRun(importer.Options{})
	if !r.UpToDate || r.NotModified != 3 || r.Downloaded != 0 {
		t.Fatalf("second import should be up to date via 304s: %+v", r)
	}

	// Change one section: only it is rewritten, readers see the new data.
	es := dnd5e()
	es[0].Translation = "ohnivá koule (nová)"
	e.up.set("/pk/sekce/167.json", es)
	r = e.mustRun(importer.Options{})
	if r.Fresh || r.MetaChanged || len(r.ChangedSections) != 1 || r.ChangedSections[0] != 167 {
		t.Fatalf("incremental import: %+v", r)
	}
	if got := e.search(model.Query{Text: "fireball"}); got.Total != 1 || got.Entries[0].Translation != "ohnivá koule (nová)" {
		t.Fatalf("store did not pick up the new file: %+v", got)
	}
	if _, err := os.Stat(filepath.Join(e.dir, "keys.db.bak")); err != nil {
		t.Errorf("no backup kept: %v", err)
	}
	e.noLeftovers()

	// Remove a section.
	e.up.set("/pk/appData.json", appData(167))
	r = e.mustRun(importer.Options{AllowShrink: true})
	if len(r.RemovedSections) != 1 || r.RemovedSections[0] != 5 {
		t.Fatalf("removal: %+v", r)
	}
	if got := e.search(model.Query{Text: "zruda"}); got.Total != 0 {
		t.Fatalf("removed section still searchable: %+v", got)
	}
}

func TestBrokenUpstreamKeepsLiveData(t *testing.T) {
	e := newEnv(t)
	e.mustRun(importer.Options{})

	for name, body := range map[string][]byte{
		"invalid json":  []byte(`[{"id": 10, "original": "Fireb`),
		"wrong section": []byte(`[{"id": 10, "original": "Fireball", "translation": "x", "section": 5}]`),
		"duplicate id":  []byte(`[{"id": 1, "original": "Fireball", "translation": "x", "section": 167}]`),
	} {
		t.Run(name, func(t *testing.T) {
			e.up.set("/pk/sekce/167.json", body)
			if _, err := e.run(importer.Options{AllowShrink: true}); err == nil {
				t.Fatal("expected import to fail")
			}
			if got := e.search(model.Query{Text: "fireball"}); got.Total != 1 || got.Entries[0].Translation != "ohnivá koule" {
				t.Fatalf("live data changed after failed import: %+v", got)
			}
			e.noLeftovers()
		})
	}
}

func TestShrinkGuard(t *testing.T) {
	e := newEnv(t)
	e.mustRun(importer.Options{})
	e.up.set("/pk/sekce/167.json", []model.RawEntry{})
	e.up.set("/pk/sekce/5.json", bytosti()[:1])
	_, err := e.run(importer.Options{})
	if err == nil || !strings.Contains(err.Error(), "refusing") {
		t.Fatalf("expected shrink guard, got %v", err)
	}
	if _, err := e.run(importer.Options{AllowShrink: true}); err != nil {
		t.Fatalf("allow-shrink: %v", err)
	}
}

func TestDryRunAndOfflineRebuild(t *testing.T) {
	e := newEnv(t)
	if r := e.mustRun(importer.Options{DryRun: true}); r.Entries != 9 {
		t.Fatalf("dry run: %+v", r)
	}
	if _, err := e.st.Status(context.Background()); err != store.ErrNoData {
		t.Fatalf("dry run created live data: %v", err)
	}
	e.mustRun(importer.Options{})

	e.srv.Close() // offline rebuild uses only the raw snapshot
	e.src.Offline = true
	if r := e.mustRun(importer.Options{Rebuild: true}); !r.Fresh || r.Entries != 9 {
		t.Fatalf("offline rebuild: %+v", r)
	}
}

func TestSearch(t *testing.T) {
	e := newEnv(t)
	e.mustRun(importer.Options{})
	ctx := context.Background()

	ids := func(r *model.SearchResult) []int {
		var out []int
		for _, en := range r.Entries {
			out = append(out, en.ID)
		}
		return out
	}
	eq := func(name string, got []int, want ...int) {
		t.Helper()
		if len(got) != len(want) {
			t.Errorf("%s: got %v, want %v", name, got, want)
			return
		}
		for i := range got {
			if got[i] != want[i] {
				t.Errorf("%s: got %v, want %v", name, got, want)
				return
			}
		}
	}

	eq("diacritics-insensitive", ids(e.search(model.Query{Text: "ZRUDA"})), 1)
	eq("czech with diacritics", ids(e.search(model.Query{Text: "ohnivá", Field: model.FieldTranslation})), 10)
	eq("field original excludes czech", ids(e.search(model.Query{Text: "drak", Field: model.FieldOriginal})))
	eq("exact ranks first", ids(e.search(model.Query{Text: "dragon"})), 3, 15, 14)
	eq("exact mode", ids(e.search(model.Query{Text: "dragon", Mode: model.ModeExact, SectionIDs: []int{167}})), 15)
	eq("prefix", ids(e.search(model.Query{Text: "magic", Mode: model.ModePrefix})), 11)
	eq("apostrophes unified", ids(e.search(model.Query{Text: "melf's"})), 12)
	eq("deprecated hidden", ids(e.search(model.Query{Text: "drobne"})))
	eq("deprecated included", ids(e.search(model.Query{Text: "drobne", IncludeDeprecated: true})), 13)
	eq("notes only with field all", ids(e.search(model.Query{Text: "kouzlo", Field: model.FieldAll})), 10)
	eq("fts", ids(e.search(model.Query{Text: "dragon NOT red", Mode: model.ModeFTS, SectionIDs: []int{167}})), 15)
	eq("fts prefix", ids(e.search(model.Query{Text: "elem*", Mode: model.ModeFTS})), 2)
	eq("empty query lists section", ids(e.search(model.Query{SectionIDs: []int{5}})), 1, 2, 3)

	groups, err := store.ResolveTags(ctx, e.st, []string{"bestiar"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	eq("tag filter", ids(e.search(model.Query{TagGroups: groups})), 15, 14)
	eq("deprecated inherits parent tags", ids(e.search(model.Query{Text: "meteory", IncludeDeprecated: true,
		TagGroups: [][]int{{247}}})), 13)

	if _, err := e.st.Search(ctx, model.Query{Text: `"unclosed`, Mode: model.ModeFTS}); err == nil {
		t.Error("invalid fts query should fail")
	}
	got, err := e.st.Get(ctx, 12)
	if err != nil || len(got.DeprecatedTranslations) != 1 || got.DeprecatedTranslations[0].ID != 13 {
		t.Errorf("get with deprecated: %+v %v", got, err)
	}
	if _, err := e.st.Get(ctx, 999); err != store.ErrNotFound {
		t.Errorf("get unknown: %v", err)
	}

	secs, err := store.ResolveSections(ctx, e.st, []string{"5e"})
	if err != nil || len(secs) != 1 || secs[0] != 167 {
		t.Errorf("resolve section 5e: %v %v", secs, err)
	}
	if _, err := store.ResolveSections(ctx, e.st, []string{"nope"}); err == nil {
		t.Error("unknown section should fail")
	}
}
