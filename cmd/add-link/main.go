package main

import (
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"log"
	"strings"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/sqlite3"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	_ "github.com/mattn/go-sqlite3"

	grab "meo.ru/rolodex/grab"
)

// add-link stores a seed URL into link_queue so the scraper can pick it up. It
// runs pending migrations first so the database is initialized on its own.
func main() {
	domainsFlag := flag.String("domains", "venture", "comma-separated analysis domains for the link")
	flag.Parse()
	args := flag.Args()
	if len(args) < 1 {
		log.Fatal("usage: add-link [-domains venture,corporate] <url>")
	}
	rawURL := args[0]
	domains := splitDomains(*domainsFlag)

	db, err := sql.Open("sqlite3", "rolodex.db?_foreign_keys=on")
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
	link, err := repo.NewLinkWithDomains(rawURL, 1, domains)
	if err != nil {
		if errors.Is(err, grab.ErrLinkExists) {
			fmt.Printf("link already exists: id=%d url=%s\n", link.ID, link.URL)
			return
		}
		log.Fatalf("NewLinkWithDomains: %v", err)
	}
	fmt.Printf("added link id=%d url=%s domains=%v\n", link.ID, link.URL, domains)
}

// splitDomains splits a comma-separated domain list into a trimmed slice,
// falling back to the single "venture" domain for an empty value.
func splitDomains(s string) []string {
	var out []string
	for _, d := range strings.Split(s, ",") {
		d = strings.TrimSpace(d)
		if d != "" {
			out = append(out, d)
		}
	}
	if len(out) == 0 {
		return []string{"venture"}
	}
	return out
}
