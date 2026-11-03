# pk — Czech RPG translation keys

This repo is the source of `pk`, a Go CLI + MCP server that mirrors the
English → Czech translation keys from https://prekladoveklice.d20.cz/ into a
local database and searches them.

## Looking up translations

Never fetch or read the site's JSON files directly (they are MBs); use `pk`.
In this repo the binary is `./bin/pk` (build with `go build -o bin/pk ./cmd/pk`).
If `pk status` says there is no data, run `pk import` first.

```sh
pk search <text>                 # substring in English + Czech, ignores case/diacritics
pk search --exact "<term>"       # whole term; best for checking one term
pk search --prefix "<start>"
pk search --fts 'word1 OR "a phrase" NOT x pref*'
pk search --cs <czech>           # reverse lookup (Czech side only); --en for English only
pk search -f all <text>          # also search notes
pk search <text> -s "D&D 5e"     # section filter: name, unique fragment ("5e") or id; repeatable (OR)
pk search -s 5e -t bestiar       # tag/book filter (repeatable, AND); text optional
pk search <text> -d              # include deprecated (superseded) translations
pk search <text> -n 50 --offset 50
pk show <id>                     # notes, tags, deprecated translations of one key
pk sections                      # ids, names, entry counts
pk tags [-s <section>] [--type tag|book]
pk status
```

- Add `--json` for structured output. Results default to 20; the footer shows
  the total, so narrow the query (section, `--exact`) instead of raising `-n`.
- The same English term often has different Czech translations per edition
  (e.g. AD&D vs 3e vs 5e): always say which section a translation comes from,
  and filter by the edition the user works with.
- Notes often carry the part of speech or category (e.g. "kouzlo" = spell).

## Developing

- `go test ./...` (tests use a fake upstream via httptest; no network).
- `go vet ./... && gofmt -l .` must be clean.
- Layout: `internal/source` (fetch, sha256/ETag, raw snapshots),
  `internal/importer` (diff/verify/swap, backend-independent),
  `internal/store` (`Store`/`Import` interfaces, name resolution),
  `internal/store/sqlite` (schema, FTS5, atomic file swap), `internal/mcpserver`,
  `internal/cli`, `internal/config`.
- Import safety is the core invariant: a new version is built beside the live
  data, verified against the source counts, then swapped atomically; failure
  leaves live data untouched. Keep that true for every backend.
- Schema changes: bump `schemaVersion` in `internal/store/sqlite/schema.go`;
  the next import rebuilds from source (no migrations).
- MCP tool output types must not be recursive (the SDK derives JSON schemas).
- Releases: push a `vX.Y.Z` tag; `.github/workflows/release.yml` runs
  GoReleaser (`.goreleaser.yaml`), which injects `internal/version` via ldflags.
- Windows cannot rename over an open file: the SQLite swap closes the store's
  own handle first; keep that in mind when touching `Commit`.
- Planned: PostgreSQL backend (staging schema + rename swap).
