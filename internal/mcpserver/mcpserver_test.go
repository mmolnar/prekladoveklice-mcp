package mcpserver

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/mmolnar/prekladoveklice-mcp/internal/model"
	"github.com/mmolnar/prekladoveklice-mcp/internal/store"
)

// fakeStore answers just enough for the tool handlers.
type fakeStore struct {
	store.Store
	lastQuery model.Query
}

func (f *fakeStore) Sections(context.Context) ([]model.Section, error) {
	return []model.Section{{ID: 3, Name: "D&D 3.5e"}, {ID: 167, Name: "D&D 5e"}}, nil
}

func (f *fakeStore) Tags(context.Context, model.TagFilter) ([]model.Tag, error) {
	return []model.Tag{{ID: 247, Name: "Příručka hráče", SectionID: 167, Type: "book"}}, nil
}

func (f *fakeStore) Search(_ context.Context, q model.Query) (*model.SearchResult, error) {
	f.lastQuery = q
	return &model.SearchResult{Total: 1, Entries: []model.Entry{{ID: 10, Original: "Fireball", Translation: "ohnivá koule", SectionID: 167, Section: "D&D 5e"}}}, nil
}

func (f *fakeStore) Get(_ context.Context, id int) (*model.Entry, error) {
	if id != 10 {
		return nil, store.ErrNotFound
	}
	return &model.Entry{ID: 10, Original: "Fireball", DeprecatedTranslations: []model.DeprecatedTranslation{{ID: 11}}}, nil
}

func connect(t *testing.T, st store.Store) *mcp.ClientSession {
	ctx := context.Background()
	ct, stt := mcp.NewInMemoryTransports()
	if _, err := New(st).Connect(ctx, stt, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

func TestTools(t *testing.T) {
	st := &fakeStore{}
	cs := connect(t, st)
	ctx := context.Background()

	tools, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools.Tools) != 4 {
		t.Fatalf("want 4 tools, got %d", len(tools.Tools))
	}

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "search_keys", Arguments: map[string]any{
		"query": "fireball", "sections": []string{"5e"}, "tags": []string{"prirucka"}, "limit": 1000,
	}})
	if err != nil || res.IsError {
		t.Fatalf("search_keys: %v %+v", err, res)
	}
	q := st.lastQuery
	if q.Text != "fireball" || len(q.SectionIDs) != 1 || q.SectionIDs[0] != 167 || q.TagGroups[0][0] != 247 || q.Limit != maxLimit {
		t.Errorf("query not translated correctly: %+v", q)
	}
	var out model.SearchResult
	b, _ := json.Marshal(res.StructuredContent)
	if err := json.Unmarshal(b, &out); err != nil || out.Total != 1 {
		t.Errorf("structured output: %s %v", b, err)
	}

	res, err = cs.CallTool(ctx, &mcp.CallToolParams{Name: "get_key", Arguments: map[string]any{"id": 999}})
	if err != nil || !res.IsError {
		t.Errorf("get_key unknown id should be a tool error: %v %+v", err, res)
	}
	res, err = cs.CallTool(ctx, &mcp.CallToolParams{Name: "search_keys", Arguments: map[string]any{"sections": []string{"nope"}}})
	if err != nil || !res.IsError {
		t.Errorf("unknown section should be a tool error: %v %+v", err, res)
	}
}
