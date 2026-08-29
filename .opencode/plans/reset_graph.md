# Reset & re-extract plan — `cmd/reconcile -reset`

Approved by user, with two additions: a **confirmation prompt** when the flag is
passed, and **a log line at each wipe step**.

## Why this is needed
`cmd/reconcile` only backfills passes where `extracted_at IS NULL`
(`facts/passes.go:133` → `ListUnexecutedPasses`). To rebuild the graph from
scratch you must (a) drop the canonical graph and (b) clear `extracted_at`.
Cascade: `entities` cascades to `entity_aliases`, `entity_mentions`,
`relations` (all `ON DELETE CASCADE`). Non-cascading: `entity_redirects` (no FK)
and `entity_fts` (standalone virtual table, only present when built with FTS5).

## Change 1 — `facts/entities.go`: `ResetGraph`
Add a method on `*EntitiesRepository`, using the `sq` library (never raw db) and
the package `log` import already present. Logs every step.

```go
// ResetGraph drops the entire canonical knowledge graph and unmarks every pass
// as extracted so the reconcile command can rebuild it from scratch. Entities
// cascade to aliases/mentions/relations; the non-cascading redirects and the
// standalone FTS index are cleared explicitly. Each step is logged.
func (r *EntitiesRepository) ResetGraph(ctx context.Context) error {
	if FTSAvailable() {
		log.Println("reset: clearing entity FTS index")
		if _, err := sq.Exec(r.db, sq.SQLite.Queryf("DELETE FROM entity_fts")); err != nil {
			return err
		}
	}
	log.Println("reset: clearing entity redirects")
	if _, err := sq.Exec(r.db, sq.SQLite.Queryf("DELETE FROM entity_redirects")); err != nil {
		return err
	}
	log.Println("reset: deleting entities (cascades aliases, mentions, relations)")
	if _, err := sq.Exec(r.db, sq.SQLite.Queryf("DELETE FROM entities")); err != nil {
		return err
	}
	log.Println("reset: unmarking every pass as extracted")
	if _, err := sq.Exec(r.db, sq.SQLite.Queryf("UPDATE passes SET extracted_at = NULL")); err != nil {
		return err
	}
	log.Println("reset: graph cleared, ready for re-extraction")
	return nil
}
```
Place it right after `markExtracted` (around `entities.go:486`).

## Change 2 — `cmd/reconcile/main.go`: `-reset` flag + confirmation
- Add imports: `bufio`, `flag`, `fmt`, `strings`.
- Register `reset := flag.Bool("reset", false, "drop all entities/relations and re-extract every pass")` and call `flag.Parse()`.
- Before `backfill`, if `*reset`: print a warning, read a line from
  `bufio.NewReader(os.Stdin)`, and require the exact word `yes` (trimmed) to
  proceed; otherwise `log.Fatal("aborted: reset not confirmed")`.
- On confirmation, `log.Println("reset confirmed; clearing graph")` then
  `entities.ResetGraph(ctx)` (fatal on error), then continue to `backfill` +
  `GlobalReconcile` as today.

```go
reset := flag.Bool("reset", false, "drop all entities/relations and re-extract every pass")
flag.Parse()
// ... migrations / logger setup unchanged ...

if *reset {
	fmt.Print("This will DROP all entities, relations and unmark every pass for re-extraction.\nType 'yes' to continue: ")
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	if strings.TrimSpace(line) != "yes" {
		log.Fatal("aborted: reset not confirmed")
	}
	log.Println("reset confirmed; clearing graph")
	if err := entities.ResetGraph(ctx); err != nil {
		log.Fatalf("reset graph: %v", err)
	}
}

backfilled, err := backfill(ctx, passes, entities)
// ... rest unchanged ...
```

## Usage
```
go run ./cmd/reconcile -reset
# prompt: Type 'yes' to continue: yes
# logs each reset step, then "backfilled N passes" / "merged M duplicate entities"
```

## Notes / optional (not in scope unless asked)
- To also re-run the LLM analyzer (not just re-extract stored results), additionally
  `DELETE FROM passes` and clear `link_queue.last_scrapped_at` so the facts machine
  re-scrapes/analyzes.
- Clearing the goqite `entities` event queue after reset is optional; stale events
  reference deleted ids. Can be added to `ResetGraph` via goqite purge if desired.
