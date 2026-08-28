package main

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
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

// add-content reads a local file and stores its contents into link_queue as if
// the page had been scraped from the internet. The file path is used as the
// link URL, and the (readable) file contents are written to both the content
// and readable_text columns.
func main() {
	if len(os.Args) < 2 {
		log.Fatal("usage: add-content <file>")
	}
	path := os.Args[1]

	raw, err := os.ReadFile(path)
	if err != nil {
		log.Fatalf("read file %q: %v", path, err)
	}

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

	// Use the file path plus a small random suffix as the link URL so each run
	// produces a distinct (non-deduplicated) link. On the rare rediscovery
	// NewLink returns the existing row together with ErrLinkExists, which we
	// ignore so the content is refreshed below.
	buf := make([]byte, 3)
	rand.Read(buf)
	suffix := hex.EncodeToString(buf)
	link, err := repo.NewLink(fmt.Sprintf("%s-%s", path, suffix), 1)
	if err != nil && !errors.Is(err, grab.ErrLinkExists) {
		log.Fatalf("NewLink: %v", err)
	}

	text := string(raw)
	if err := repo.SaveScrapeResult(link.ID, text, text); err != nil {
		log.Fatalf("SaveScrapeResult: %v", err)
	}

	fmt.Printf("added content from %s as link id=%d\n", path, link.ID)
}
