package main

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/sqlite3"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	_ "github.com/mattn/go-sqlite3"

	grab "meo.ru/rolodex/grab"
)

// add-link stores a seed URL into link_queue so the scraper can pick it up. It
// runs pending migrations first so the database is initialized on its own.
func main() {
	if len(os.Args) < 2 {
		log.Fatal("usage: add-link <url>")
	}
	rawURL := os.Args[1]

	db, err := sql.Open("sqlite3", "rolodex.db")
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	// Apply pending migrations so the database is initialized regardless of
	// whether the main application has run yet.
	m, err := migrate.New("file://migrations", "sqlite3://rolodex.db")
	if err != nil {
		log.Fatal(err)
	}
	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		log.Fatal(err)
	}

	repo := grab.NewLinksRepository(db)
	link, err := repo.NewLink(rawURL, 1)
	if err != nil {
		if errors.Is(err, grab.ErrLinkExists) {
			fmt.Printf("link already exists: id=%d url=%s\n", link.ID, link.URL)
			return
		}
		log.Fatalf("NewLink: %v", err)
	}
	fmt.Printf("added link id=%d url=%s\n", link.ID, link.URL)
}
