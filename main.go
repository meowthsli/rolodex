package main

import (
	"database/sql"
	"fmt"
	"log"

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

	_, err = sq.Exec(db, sq.SQLite.InsertInto(LQ).Columns(LQ.URL).Values("https://example.com"))
	if err != nil {
		log.Fatal(err)
	}

	links, err := sq.FetchAll(db, sq.SQLite.From(LQ), Mapper)
	if err != nil {
		log.Fatal(err)
	}

	fmt.Printf("link_queue rows: %+v\n", links)
}
