package main

import (
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

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

	repo := NewLinksRepository(db)

	link, err := repo.AddLink("https://example.com")
	if err != nil {
		log.Fatal(err)
	}

	links, err := repo.ListLinks()
	if err != nil {
		log.Fatal(err)
	}
	_ = link

	fmt.Printf("link_queue rows: %+v\n", links)

	scraper := NewScraper(repo, &http.Client{}, 5*time.Second)
	scraper.Start()
	defer scraper.Stop()

	fmt.Println("scraper running (tick: 5s); press Ctrl+C to stop")

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
}
