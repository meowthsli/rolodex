package main

import (
	"database/sql"
	"flag"
	"fmt"
	"log"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/sqlite3"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	_ "github.com/mattn/go-sqlite3"

	sq "github.com/bokwoon95/sq"
)

// clear-events deletes pending messages from the goqite entity event queue(s)
// in the database. It runs pending migrations first so the goqite table exists
// even if the main application has never run.
func main() {
	queue := flag.String("queue", "entities", "goqite queue name to clear; empty string clears every queue")
	dbPath := flag.String("db", "rolodex.db", "path to the sqlite database")
	flag.Parse()

	db, err := sql.Open("sqlite3", *dbPath)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	// Apply pending migrations so the goqite table is initialized on its own.
	m, err := migrate.New("file://migrations", "sqlite3://"+*dbPath)
	if err != nil {
		log.Fatal(err)
	}
	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		log.Fatal(err)
	}

	var res sq.Result
	if *queue == "" {
		res, err = sq.Exec(db, sq.SQLite.Queryf("DELETE FROM goqite"))
	} else {
		res, err = sq.Exec(db, sq.SQLite.Queryf("DELETE FROM goqite WHERE queue = {}", *queue))
	}
	if err != nil {
		log.Fatalf("clear events: %v", err)
	}
	n := res.RowsAffected
	if *queue == "" {
		fmt.Printf("cleared %d entity event message(s) from all queues\n", n)
	} else {
		fmt.Printf("cleared %d entity event message(s) from queue %q\n", n, *queue)
	}
}
