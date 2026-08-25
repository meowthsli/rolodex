package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	sq "github.com/bokwoon95/sq"
	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/sqlite3"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	_ "github.com/mattn/go-sqlite3"

	facts "meo.ru/rolodex/facts"
	grab "meo.ru/rolodex/grab"
)

func main() {
	// Apply all pending database migrations (create/update tables) against the
	// local sqlite database file. Migrations live in the ./migrations directory
	// and are versioned; Up is idempotent (ErrNoChange is expected on reruns).
	m, err := migrate.New("file://migrations", "sqlite3://rolodex.db")
	if err != nil {
		log.Fatal(err)
	}
	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		log.Fatal(err)
	}

	// Open the (now migrated) database handle. Only the go-sqlite3 driver is
	// used, per project conventions.
	db, err := sql.Open("sqlite3", "rolodex.db")
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

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

	// The link repository owns the scraped-content table (link_queue) and is
	// shared by both the scraper and the analysis pipeline below.
	repo := grab.NewLinksRepository(db)

	// NewLink dedupes by normalized URL; an already-existing link is not an
	// error, we simply skip it. Left commented as an example of how to seed a
	// starting URL.
	//_, err = repo.NewLink("https://github.com/Gelembjuk/articletext")
	//if err != nil && !errors.Is(err, grab.ErrLinkExists) {
	//	log.Fatal(err)
	//}

	// Snapshot the queue before processing starts, for visibility.
	links, err := repo.ListLinks()
	if err != nil {
		log.Fatal(err)
	}

	fmt.Printf("link_queue rows: %+v\n", links)

	// Scraper: every tick it picks the next unscraped link, fetches its page
	// over HTTP, extracts readable text, stores it, and discovers new links.
	scraper := grab.NewScraper(repo, &http.Client{}, 1*time.Second)
	scraper.Start()
	defer scraper.Stop()

	// Analysis pipeline: every tick it picks a link that has been scraped
	// (last_scrapped_at set) but has no pass yet, runs its readable text through
	// the analyzer, and stores the result as a pass. Uses a mock analyzer until
	// a real one is wired in.
	fm := facts.NewFactsMachine(db, facts.MockAnalyzer{Result: `{"mock":true}`}, 5*time.Second)
	fm.Start()
	defer fm.Stop()

	fmt.Println("scraper + facts machine running (tick: 5s); press Ctrl+C to stop")

	// Block until the process receives SIGINT (Ctrl+C) or SIGTERM, then shut
	// down the background loops via their deferred Stop calls.
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop

	// Final snapshot of the queue after processing, for visibility.
	links, err = repo.ListLinks()
	if err != nil {
		log.Fatal(err)
	}

	fmt.Printf("link_queue rows: %+v\n", links)
}
