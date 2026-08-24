package main

import (
	"database/sql"
	"fmt"
	"log"

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
}
