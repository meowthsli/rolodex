package main

import (
	"database/sql"
	"fmt"
	"log"

	_ "modernc.org/sqlite"
)

const createScript = `
CREATE TABLE IF NOT EXISTS contacts (
	id INTEGER PRIMARY KEY AUTOINCREMENT,
	name TEXT NOT NULL,
	phone TEXT,
	email TEXT
);
`

func main() {
	db, err := sql.Open("sqlite", "rolodex.db")
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	if _, err := db.Exec(createScript); err != nil {
		log.Fatal(err)
	}

	fmt.Println("rolodex.db created")
}
