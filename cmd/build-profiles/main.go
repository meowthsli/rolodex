package main

import (
	"database/sql"
	"flag"
	"log"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/sqlite3"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	_ "github.com/mattn/go-sqlite3"

	profs "meo.ru/rolodex/profiles"
)

// build-profiles pre-computes the long-text profile document for every entity
// and stores it in the entity_profiles table. It is a backfill intended to run
// after reconcile (or on a fresh database) so the profile tab is populated; the
// running rolodex service also rebuilds a single profile whenever an entity
// event arrives. Safe to re-run: existing profiles are overwritten.
func main() {
	dbPath := flag.String("db", "rolodex.db", "path to the sqlite database")
	flag.Parse()

	db, err := sql.Open("sqlite3", *dbPath+"?_foreign_keys=on")
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	m, err := migrate.New("file://migrations", "sqlite3://"+*dbPath)
	if err != nil {
		log.Fatal(err)
	}
	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		log.Fatal(err)
	}

	profiles := profs.NewProfilesRepository(db)
	built, err := profiles.RebuildAll()
	if err != nil {
		log.Fatalf("rebuild profiles: %v", err)
	}
	log.Printf("built %d entity profiles", built)
}
