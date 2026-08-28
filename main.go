package main

import (
	"bufio"
	"context"
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	sq "github.com/bokwoon95/sq"
	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/sqlite3"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	_ "github.com/mattn/go-sqlite3"

	facts "meo.ru/rolodex/facts"
	grab "meo.ru/rolodex/grab"
)

// loadEnvFile parses a KEY=VALUE .env file and exports each entry into the
// process environment (existing variables are not overwritten). A missing file
// is not an error: the caller may already have the values set elsewhere.
func loadEnvFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		idx := strings.Index(line, "=")
		if idx < 0 {
			continue
		}
		key := strings.TrimSpace(line[:idx])
		val := strings.Trim(strings.TrimSpace(line[idx+1:]), `"'`)
		if os.Getenv(key) == "" {
			os.Setenv(key, val)
		}
	}
	return sc.Err()
}

func main() {
	// Load credentials/endpoints from .env (KEY=VALUE) before anything else so
	// the values are available for configuring the analyzer below.
	if err := loadEnvFile(".env"); err != nil {
		log.Fatalf("load .env: %v", err)
	}

	// Apply all pending database migrations (create/update tables) against the
	// local sqlite database file. Migrations live in the ./migrations directory
	// and are versioned; Up is idempotent (ErrNoChange is expected on reruns).
	m, err := migrate.New("file://migrations", "sqlite3://rolodex.db")
	if err != nil {
		log.Fatal(err)
	}
	if err := m.Up(); err != nil && err != migrate.ErrNoChange {
		log.Fatal(err)
	}

	// Open the (now migrated) database handle. Only the go-sqlite3 driver is
	// used, per project conventions.
	db, err := sql.Open("sqlite3", "rolodex.db")
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	// Probe FTS5 support once at app start. This provisions the full-text index
	// used for fuzzy entity matching and records availability globally, so the
	// check is never repeated when repositories are created or recreated.
	facts.InitFTS(db)

	// Install a query logger so every SQL statement (with timing) is printed to
	// stdout. Args are hidden to avoid dumping raw content into the logs.
	logger := sq.NewLogger(os.Stdout, "", log.LstdFlags, sq.LoggerConfig{
		ShowTimeTaken: true,
		HideArgs:      true,
	})
	sq.SetDefaultLogQuery(func(ctx context.Context, queryStats sq.QueryStats) {
		// You can choose to only log queries if they encountered an error.
		// if queryStats.Err == nil {
		//     return
		// }
		logger.SqLogQuery(ctx, queryStats)
	})

	// The link repository owns the scraped-content table (link_queue) and is
	// shared by both the scraper and the analysis pipeline below.
	repo := grab.NewLinksRepository(db)

	// NewLink dedupes by normalized URL; an already-existing link is not an
	// error, we simply skip it. Left commented as an example of how to seed a
	// starting URL.
	//_, err = repo.NewLink("https://github.com/Gelembjuk/articletext")
	//if err != nil && !errors.Is(err, grab.ErrLinkExists) {
	//	log.Fatal(err)
	//}

	// Snapshot the queue before processing starts, for visibility.
	links, err := repo.ListLinks()
	if err != nil {
		log.Fatal(err)
	}

	fmt.Printf("link_queue rows: %+v\n", links)

	// Scraper: every tick it picks the next unscraped link, fetches its page
	// over HTTP, extracts readable text, stores it, and discovers new links.
	scraper := grab.NewScraper(repo, &http.Client{}, 1*time.Second)
	scraper.Start()
	defer scraper.Stop()

	// Analysis pipeline: every tick it picks a link that has been scraped
	// (last_scrapped_at set) but has no pass yet, runs its readable text through
	// the analyzer, and stores the result as a pass. The analyzer talks to an
	// external OpenAI-compatible LLM whose URL and bearer key come from .env
	// (llm_api_url / llm_api_key).
	llmURL := os.Getenv("llm_api_url")
	llmKey := os.Getenv("llm_api_key")
	if llmURL == "" || llmKey == "" {
		log.Fatal("llm_api_url and llm_api_key must be set (in .env or environment)")
	}
	fm := facts.NewFactsMachine(db, facts.NewOpenAIAnalyzer(llmURL, llmKey), 1*time.Second, "facts",
		facts.NewGoqiteEntityPublisher(db))

	// Entity event handler: consume lifecycle events (create/merge) published by
	// the facts machine and print each one. Cancelled on shutdown.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go facts.StartEntityEventHandler(ctx, db, func(ev facts.EntityEvent) {
		log.Printf("entity event: id=%d name=%q", ev.ID, ev.Name)
	})

	if v := os.Getenv("llm_chunk_size"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			fm.Chunker.MaxRunes = n
		}
	}
	if v := os.Getenv("llm_chunk_overlap"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			fm.Chunker.OverlapRunes = n
		}
	}
	fm.Start()
	defer fm.Stop()

	fmt.Println("scraper + facts machine running (tick: 5s); press Ctrl+C to stop")

	// Block until the process receives SIGINT (Ctrl+C) or SIGTERM, then shut
	// down the background loops via their deferred Stop calls.
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop

	// Final snapshot of the queue after processing, for visibility.
	links, err = repo.ListLinks()
	if err != nil {
		log.Fatal(err)
	}

	fmt.Printf("link_queue rows: %+v\n", links)
}
