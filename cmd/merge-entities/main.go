package main

import (
	"bufio"
	"database/sql"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/sqlite3"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	_ "github.com/mattn/go-sqlite3"

	facts "meo.ru/rolodex/facts"
)

// merge-entities manually folds one entity (the slave) into another (the master).
// It checks both ids exist, asks for confirmation, and merges the slave into the
// master, forcing the master to survive.
func main() {
	masterID := flag.Int("master", 0, "id of the entity that should survive the merge")
	slaveID := flag.Int("slave", 0, "id of the entity to merge into the master")
	dbPath := flag.String("db", "rolodex.db", "path to the sqlite database")
	flag.Parse()

	if *masterID <= 0 || *slaveID <= 0 {
		log.Fatal("usage: merge-entities -master <id> -slave <id> [-db rolodex.db]")
	}

	db, err := sql.Open("sqlite3", *dbPath)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	// Apply pending migrations so the entities table (and FTS index) exists.
	m, err := migrate.New("file://migrations", "sqlite3://"+*dbPath)
	if err != nil {
		log.Fatal(err)
	}
	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		log.Fatal(err)
	}
	// Provision the FTS index (no-op without fts5) so alias/name reconciliation
	// behaves like production.
	facts.InitFTS(db)

	repo := facts.NewEntitiesRepository(db, facts.NoopEntityPublisher{})

	master, err := repo.GetEntity(*masterID)
	if err != nil {
		log.Fatalf("master entity %d not found: %v", *masterID, err)
	}
	slave, err := repo.GetEntity(*slaveID)
	if err != nil {
		log.Fatalf("slave entity %d not found: %v", *slaveID, err)
	}

	fmt.Printf("Master (survivor) %d: %q types=%v\n", master.ID, master.DisplayName, master.Types)
	fmt.Printf("Slave (to merge) %d: %q types=%v\n", slave.ID, slave.DisplayName, slave.Types)
	fmt.Printf("Merge slave %d into master %d? [y/N] ", slave.ID, master.ID)

	reader := bufio.NewReader(os.Stdin)
	answer, err := reader.ReadString('\n')
	if err != nil {
		log.Fatalf("read confirmation: %v", err)
	}
	answer = strings.ToLower(strings.TrimSpace(answer))
	if answer != "y" && answer != "yes" {
		fmt.Println("aborted; no changes made")
		return
	}

	survivor, err := repo.MergeEntities(*masterID, *slaveID)
	if err != nil {
		log.Fatalf("merge failed: %v", err)
	}
	fmt.Printf("merged: master %d now %q (types=%v)\n", survivor.ID, survivor.DisplayName, survivor.Types)
}
