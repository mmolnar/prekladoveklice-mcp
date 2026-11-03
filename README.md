# pk — Czech RPG translation keys, searchable offline and over MCP

`pk` keeps a local, searchable copy of the translation keys published by
[Překladové klíče](https://prekladoveklice.d20.cz/) — the community
English → Czech glossaries for D&D (AD&D to 5e), Forgotten Realms, Dragonlance,
Fate, Dungeon World and more — and serves them

- on the **command line** (`pk search fireball`), and
- to AI assistants such as Claude as an **MCP server** (`pk mcp`), locally over
  stdio or hosted over Streamable HTTP.

It is a single static binary with no runtime dependencies.

> The translation keys are the work of the Překladové klíče authors and
> community. This repository contains only the tool; it does not include or
> redistribute the data. `pk import` downloads it from the website.

[Česky: jak to používat lokálně ↓](#česky-rychlý-start)

## Install

Download a binary for your platform from the
[releases page](https://github.com/your-github-user/prekladoveklice-mcp/releases),
or build from source (Go 1.26+):

```sh
go install github.com/your-github-user/prekladoveklice-mcp/cmd/pk@latest
```

## Quick start

```sh
pk import                 # download everything (~4.5 MB), build the database
pk sections               # what dictionaries exist
pk search fireball        # English or Czech, case- and diacritics-insensitive
```

```
#552  Fireball → ohnivá koule  [D&D 3e; core]
#15491  Fireball → Ohnivá koule  [AD&D; kouzlo]
#48374  Fireball → ohnivá koule  [13th Age]
#56125  Fireball → Ohnivá koule  [Dungeon World; Kouzelník; DW] — kouzlo
#57729  Fireball → ohnivá koule  [D&D 5e; Příručka hráče] — kouzlo
-- 1-5 of 11 matches (use --limit/--offset for more)
```

Run `pk import` again any time to update: it only rewrites what changed.

## Searching

```sh
pk search fireball                          # substring (default) in English and Czech
pk search --exact "magic missile"           # whole string
pk search --prefix "wall of"                # starts with
pk search --fts 'dragon NOT red'            # full-text: words, "phrases", OR, NOT, prefix*
pk search --cs zruda                        # only the Czech side (zruda finds zrůda)
pk search --en aberration                   # only the English side
pk search -f all kouzlo                     # also search the notes
pk search fireball -s "D&D 5e"              # restrict to sections (name, unique part, or id)
pk search -s 5e -t bestiar -n 100           # filter by tag or book, no text needed
pk search meteors --deprecated              # include superseded translations
pk search dragon -n 50 --offset 50          # paging
pk show 62472                               # one entry with notes, tags, old translations
pk tags -s 5e                               # tags and books of a section
pk tags --type book
```

- Matching ignores case, diacritics and quote styles (`melf's` finds `Melf’s`).
- Results are ranked exact match → prefix → word start → anywhere.
- `--section` may be repeated (any of them); `--tag` may be repeated (all of
  them). A tag name that exists in several sections matches in all of them.
- Every command accepts `--json` for machine-readable output.

## Updating safely

`pk import`:

1. downloads `appData.json` and every `sekce/<id>.json`, using `ETag` so
   unchanged files are not transferred again;
2. compares each file's sha256 with the previous import and rewrites only the
   sections that changed (plus removes sections that disappeared);
3. builds the result **next to** the live database and verifies it: entry
   count of every section against the source, tag references, SQLite and
   full-text index integrity, and a test search;
4. only then atomically swaps it in, keeping the previous database as
   `keys.db.bak`.

If anything fails, the live data is untouched. An update that would remove more
than 20 % of all entries is refused unless you pass `--allow-shrink` (protects
against a broken upstream). Other flags: `--dry-run`, `--rebuild`, and
`--offline` (rebuild from the raw JSON snapshot kept in the data directory).

## Using it with Claude (MCP)

Claude Code:

```sh
claude mcp add --scope user pk -- pk mcp
```

Claude Desktop (`claude_desktop_config.json`):

```json
{
  "mcpServers": {
    "pk": { "command": "/path/to/pk", "args": ["mcp"] }
  }
}
```

Tools: `search_keys`, `get_key`, `list_sections`, `list_tags`. Run `pk import`
once first (or set `mcp.import_on_start: true`).

MCP is optional. In Claude Code, the CLI works just as well: put a note like
this into your project's `CLAUDE.md`:

```markdown
For Czech translations of RPG terms use the `pk` CLI (prekladoveklice.d20.cz):
`pk search <term> [-s "D&D 5e"] [--exact|--fts] [--cs|--en] [--json]`, `pk show <id>`,
`pk sections`, `pk tags -s <section>`. Results are capped (-n); prefer narrow queries.
```

## Hosting an MCP server

`pk mcp` can serve [Streamable HTTP](https://modelcontextprotocol.io/specification/2025-06-18/basic/transports#streamable-http)
for many users and refresh the data on its own:

```yaml
# server.yaml
mcp:
  transport: http
  listen: 0.0.0.0:8080
  path: /mcp
  refresh_interval: 24h
  import_on_start: true
```

```sh
pk mcp --config server.yaml     # health check: GET /healthz
```

Put it behind a reverse proxy for TLS. The server is read-only.

## Configuration

No config file is needed for local use. `pk config --example` prints a
commented example; `pk config` shows the effective settings. Lookup order:
`--config`, `$PK_CONFIG`, `./pk.yaml`, `<user config dir>/pk/config.yaml`.
Environment overrides: `PK_BACKEND`, `PK_DATA_DIR`, `PK_SQLITE_PATH`,
`PK_POSTGRES_DSN`; config values may reference `${ENV_VARS}`.

Data lives in `~/.local/share/pk` (`%LOCALAPPDATA%\pk` on Windows):
`keys.db`, `keys.db.bak`, and `raw/` with the downloaded JSON.

Backends: `sqlite` (default, single file) is ready; `postgres` is planned for
hosted deployments.

## Development

```sh
go test ./...
go build -o bin/pk ./cmd/pk
```

Layout: `cmd/pk` (entry point), `internal/source` (download, sha/etag,
snapshots), `internal/importer` (diff, verify, swap — backend independent),
`internal/store` (storage interface) and `internal/store/sqlite`,
`internal/mcpserver`, `internal/cli`.

## License

MIT — see [LICENSE](LICENSE). This covers the code only, not the translation
keys, which belong to their authors at prekladoveklice.d20.cz.

---

## Česky: rychlý start

`pk` stáhne [Překladové klíče](https://prekladoveklice.d20.cz/) do počítače a
umožní v nich rychle hledat z příkazové řádky nebo z Clauda (přes MCP). Data
zůstávají dílem autorů webu; nástroj je jen stahuje.

1. Stáhněte `pk` pro svůj systém ze
   [stránky s vydáními](https://github.com/your-github-user/prekladoveklice-mcp/releases)
   a dejte ho někam do `PATH`.
2. Stáhněte klíče (stejným příkazem je později aktualizujete — stahuje se jen
   to, co se změnilo, a stará data se nezahodí, dokud nová neprojdou kontrolou):

   ```sh
   pk import
   ```

3. Hledejte (velikost písmen ani diakritika nehrají roli):

   ```sh
   pk search fireball                  # anglicky i česky
   pk search --cs ohniva koule         # jen v českých překladech
   pk search --exact "magic missile" -s "D&D 5e"
   pk sections                         # seznam sekcí
   pk tags -s 5e                       # štítky a knihy sekce
   pk show 57729                       # detail klíče
   ```

4. Volitelně pro Claude Code: `claude mcp add --scope user pk -- pk mcp`
