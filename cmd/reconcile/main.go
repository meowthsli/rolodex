package main

import (
	"context"
	"database/sql"
	"log"
	"os"

	"github.com/bokwoon95/sq"
	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/sqlite3"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	_ "github.com/mattn/go-sqlite3"

	"meo.ru/rolodex/facts"
)

// reconcile extracts entities from every not-yet-processed pass (backfill) and
// then runs the global entity merge until the canonical graph is stable. It can
// be run repeatedly and is safe to re-run.
func main() {
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

	// Install a query logger so every SQL statement (with timing) is printed to
	// stdout. Args are hidden to avoid dumping raw content into the logs.
	logger := sq.NewLogger(os.Stdout, "", log.LstdFlags, sq.LoggerConfig{
		ShowTimeTaken: true,
		HideArgs:      true,
	})
	sq.SetDefaultLogQuery(func(ctx context.Context, queryStats sq.QueryStats) {
		// You can choose to only log queries if they encountered an error.
		// if queryStats.Err == nil {
		//     return
		// }
		logger.SqLogQuery(ctx, queryStats)
	})

	passes := facts.NewPassesRepository(db)
	entities := facts.NewEntitiesRepository(db)
	ctx := context.Background()

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
