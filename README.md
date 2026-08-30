# Rolodex

A scraper + facts machine that crawls links, extracts readable text, and builds a
canonical knowledge graph — entities (people, organizations, dates, …) and the
relations between them (founded, invested, employed-at, …) — from the content via
an LLM. State lives in a single SQLite database (`rolodex.db`); schema is managed by
the migrations under `./migrations`.

Build everything (outputs go to `bin/`):

```
make build          # builds all executables with -tags sqlite_fts5
make test           # runs the full test suite
make vet            # go vet
```

All executables open `rolodex.db` and apply pending migrations on startup, so the
database is initialized automatically the first time any of them runs.

## Executables

### rolodex
The long-running service: scrapes pending links, feeds readable text to the LLM
for entity and relation extraction, reconciles entities (and redirects their
relations onto the survivor), and publishes entity lifecycle events.

Flags:
- `-sqlog` — log every SQL query (with timing) to stdout. Off by default.
- `-nolinks` — dry-run spider: print discovered links to stdout instead of
  inserting them into `link_queue`.

Files / env read:
- `.env` (KEY=VALUE, optional): `llm_api_url`, `llm_api_key` (required), and
  optional `llm_chunk_size`, `llm_chunk_overlap` to tune text chunking.
- `blacklist.txt` — one banned URL prefix per line (scheme-less). A link whose
  stored URL starts with any prefix is erased before being fetched. `#` lines are
  comments. Missing file is fine; an unreadable file is warned about and ignored.
- `rolodex.db` — the database.

### add-link
Seed the crawler with a starting URL.

Usage: `add-link <url>`
- `<url>` — URL to enqueue (deduplicated by normalized form; rediscovery
  re-queues the existing row).

### add-content
Inject a local file into `link_queue` as if it had been scraped from the web.

Usage: `add-content <file>`
- `<file>` — path to a text file; its contents become both the raw `content` and
  the `readable_text`. A random suffix is added to the URL so each run is distinct.

### reconcile
Backfill entity and relation extraction for every not-yet-processed pass and then
merge duplicate entities until the graph is stable. Safe to re-run.

Usage: `reconcile [-reset]`
- `-reset` — **destructive**: drops the entire knowledge graph (entities, their
  aliases/mentions/relations and the FTS index) and unmarks every pass as
  extracted, then rebuilds everything from the stored pass results. It asks for
  `yes` confirmation on stdin before wiping anything.
- Logs every SQL query to stdout. Publishes entity lifecycle events to the goqite
  queue (requires `rolodex.db` to be migrated).

### clear-events
Delete pending messages from the goqite entity event queue.

Flags:
- `-queue` — queue name to clear (default `entities`). Empty string clears every
  queue.
- `-db` — database path (default `rolodex.db`).

### merge-entities
Manually merge one entity into another, forcing the master to survive.

Flags:
- `-master <id>` — id of the entity that should survive (required).
- `-slave <id>` — id of the entity to merge into the master (required).
- `-db` — database path (default `rolodex.db`).

Prompts for `[y/N]` confirmation on stdin before merging. Both ids must exist.

## Dashboard (read-only)

A Python + Streamlit web view of the knowledge graph. It only reads `rolodex.db`
(entities, relations, passes, link queue) and never writes — you can point it at
a live database while the Go service runs.

Setup and launch:

```
make install-dashboard   # installs dashboard/requirements.txt into ./venv
make dashboard           # runs streamlit, opens http://localhost:8501
```

Or run directly: `./venv/bin/streamlit run dashboard/app.py`

Tabs:
- **Entities** — table sorted by promotion score, plus a detail view with
  aliases and all in/out relations for a selected entity.
- **Relations** — source / type / target listing.
- **Graph** — spring-layout knowledge graph of the top-N entities.
- **Passes** — extraction passes with domain, errors, and source URL.
- **Links** — the crawl queue with status (ok/error) per link.

The database path defaults to `rolodex.db`; override with the `ROLODEX_DB`
environment variable. Deps live in `dashboard/requirements.txt`.

## Scraper rules (rolodex)
For reference, the crawler enforces:
- **Max depth**: link discovery only runs for generations ≤ 2.
- **Blacklist**: banned URLs are erased before fetching.
- **Min URL length**: links whose raw href is shorter than 6 chars (e.g. `/exit`,
  `#`, `#frag`) are never extracted.
- **Min content size**: pages under 1 KB are not stored (recorded as an error).
- **Readability**: `href` targets are neutralized (`href="#"`) before text
  extraction so raw URLs don't leak into the readable content.
