package main

import (
	"bufio"
	"context"
	"database/sql"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/bokwoon95/sq"
	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/sqlite3"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	_ "github.com/mattn/go-sqlite3"

	"meo.ru/rolodex/facts"
)

// reconcile extracts entities from every not-yet-processed pass (backfill) and
// then runs the global entity merge until the canonical graph is stable. It can
// be run repeatedly and is safe to re-run. With -reset it first drops the whole
// graph and unmarks passes, rebuilding everything from the stored pass results.
func main() {
	reset := flag.Bool("reset", false, "drop all entities/relations and re-extract every pass")
	sqlog := flag.Bool("sqlog", false, "print SQL query logs (with timing) to stdout")
	flag.Parse()

	db, err := sql.Open("sqlite3", "rolodex.db")
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	m, err := migrate.New("file://migrations", "sqlite3://rolodex.db")
	if err != nil {
		log.Fatal(err)
	}
	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		log.Fatal(err)
	}

	// Install a query logger only when -sqlog is passed, so SQL statements (with
	// timing) are printed to stdout. Args are hidden to avoid dumping raw content
	// into the logs. Without the flag, queries are not logged.
	if *sqlog {
		logger := sq.NewLogger(os.Stdout, "", log.LstdFlags, sq.LoggerConfig{
			ShowTimeTaken: true,
			HideArgs:      true,
		})
		sq.SetDefaultLogQuery(func(ctx context.Context, queryStats sq.QueryStats) {
			logger.SqLogQuery(ctx, queryStats)
		})
	}

	passes := facts.NewPassesRepository(db)
	entities := facts.NewEntitiesRepository(db, facts.NewGoqiteEntityPublisher(db))
	ctx := context.Background()

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
	if err != nil {
		log.Fatalf("backfill: %v", err)
	}
	log.Printf("backfilled %d passes", backfilled)

	merged, err := entities.GlobalReconcile(ctx)
	if err != nil {
		log.Fatalf("global reconcile: %v", err)
	}
	log.Printf("merged %d duplicate entities", merged)
}

// backfill extracts entities from every pass that has not been extracted yet.
func backfill(ctx context.Context, passes *facts.PassesRepository, entities *facts.EntitiesRepository) (int, error) {
	unextracted, err := passes.ListUnextractedPasses()
	if err != nil {
		return 0, err
	}
	for _, p := range unextracted {
		if err := entities.ExtractPass(ctx, p); err != nil {
			log.Printf("extract pass id=%d: %v", p.ID, err)
			continue
		}
	}
	return len(unextracted), nil
}
