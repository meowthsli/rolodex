package main

import (
	"context"
	"database/sql"
	"errors"
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
)

func main() {
	m, err := migrate.New("file://migrations", "sqlite3://rolodex.db")
	if err != nil {
		log.Fatal(err)
	}
	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		log.Fatal(err)
	}

	db, err := sql.Open("sqlite3", "rolodex.db")
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

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

	repo := NewLinksRepository(db)

	// NewLink dedupes by normalized URL; an already-existing link is not an
	// error, we simply skip it.
	_, err = repo.NewLink("https://localhost")
	if err != nil && !errors.Is(err, ErrLinkExists) {
		log.Fatal(err)
	}

	links, err := repo.ListLinks()
	if err != nil {
		log.Fatal(err)
	}

	fmt.Printf("link_queue rows: %+v\n", links)

	scraper := NewScraper(repo, &http.Client{}, 1*time.Second)
	scraper.Start()
	defer scraper.Stop()

	fmt.Println("scraper running (tick: 5s); press Ctrl+C to stop")

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop

	links, err = repo.ListLinks()
	if err != nil {
		log.Fatal(err)
	}

	fmt.Printf("link_queue rows: %+v\n", links)
}
