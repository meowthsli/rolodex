package main

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"

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
	domainsFlag := flag.String("domains", "venture", "comma-separated analysis domains for the link")
	flag.Parse()
	args := flag.Args()
	if len(args) < 1 {
		log.Fatal("usage: add-content [-domains venture,corporate] <file>")
	}
	path := args[0]
	domains := splitDomains(*domainsFlag)

	raw, err := os.ReadFile(path)
	if err != nil {
		log.Fatalf("read file %q: %v", path, err)
	}

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

	// Use the file path plus a small random suffix as the link URL so each run
	// produces a distinct (non-deduplicated) link. On the rare rediscovery
	// NewLinkWithDomains returns the existing row together with ErrLinkExists,
	// which we ignore so the content is refreshed below.
	buf := make([]byte, 3)
	rand.Read(buf)
	suffix := hex.EncodeToString(buf)
	link, err := repo.NewLinkWithDomains(fmt.Sprintf("%s-%s", path, suffix), 1, domains)
	if err != nil && !errors.Is(err, grab.ErrLinkExists) {
		log.Fatalf("NewLinkWithDomains: %v", err)
	}

	text := string(raw)
	if err := repo.SaveScrapeResult(link.ID, text, text); err != nil {
		log.Fatalf("SaveScrapeResult: %v", err)
	}

	fmt.Printf("added content from %s as link id=%d domains=%v\n", path, link.ID, domains)
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
