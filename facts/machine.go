package facts

import (
	"context"
	"database/sql"
	"log"
	"time"
)

// Analyzer turns a single link's readable text into a structured analysis
// result. The returned string is stored verbatim as JSON in passes.result.
// Implementations may call external models, rule engines, etc.
type Analyzer interface {
	Analyze(ctx context.Context, content string) (string, error)
}

// MockAnalyzer is a placeholder Analyzer that returns a fixed, pre-programmed
// answer for every request. It lets the pipeline be exercised end-to-end
// before a real analyzer is wired in.
type MockAnalyzer struct {
	// Result is the JSON string returned for every Analyze call.
	Result string
	// Err, if set, is returned instead of Result.
	Err error
}

// Analyze implements Analyzer, ignoring the input and returning the
// pre-programmed response.
func (m MockAnalyzer) Analyze(ctx context.Context, content string) (string, error) {
	return m.Result, m.Err
}

// FactsMachine periodically pulls the next link whose readable text has not yet
// been analyzed, runs it through an Analyzer, and persists the result as a pass.
type FactsMachine struct {
	db       *sql.DB
	analyzer Analyzer
	passes   *PassesRepository
	tick     time.Duration
	stop     chan struct{}
}

// NewFactsMachine builds a FactsMachine. A non-positive tick defaults to 5s.
func NewFactsMachine(db *sql.DB, analyzer Analyzer, tick time.Duration) *FactsMachine {
	if tick <= 0 {
		tick = 5 * time.Second
	}
	return &FactsMachine{db: db, analyzer: analyzer, passes: NewPassesRepository(db), tick: tick}
}

// Start launches the analysis loop in a background goroutine.
func (m *FactsMachine) Start() {
	log.Printf("facts machine started (tick: %s)", m.tick)
	m.stop = make(chan struct{})
	go m.run()
}

// Stop terminates the analysis loop.
func (m *FactsMachine) Stop() {
	if m.stop != nil {
		close(m.stop)
		m.stop = nil
	}
}

func (m *FactsMachine) run() {
	ticker := time.NewTicker(m.tick)
	defer ticker.Stop()
	for {
		select {
		case <-m.stop:
			log.Println("facts machine stopped")
			return
		case <-ticker.C:
			if err := m.ProcessOnce(context.Background()); err != nil {
				log.Printf("facts machine error: %v", err)
			}
		}
	}
}

// nextUnprocessedLink returns the id and readable text of a link that has been
// scraped (last_scrapped_at set) but has no pass yet. found is false when
// nothing is left to analyze.
func (m *FactsMachine) nextUnprocessedLink(ctx context.Context) (id int, readable string, found bool, err error) {
	row := m.db.QueryRowContext(ctx,
		"SELECT lq.id, lq.readable_text FROM link_queue lq "+
			"LEFT JOIN passes p ON p.link_queue_id = lq.id "+
			"WHERE lq.last_scrapped_at IS NOT NULL AND p.id IS NULL "+
			"LIMIT 1")
	var rid int
	var rtext string
	if serr := row.Scan(&rid, &rtext); serr != nil {
		if serr == sql.ErrNoRows {
			return 0, "", false, nil
		}
		return 0, "", false, serr
	}
	return rid, rtext, true, nil
}

// ProcessOnce analyzes a single unprocessed link and stores the result. It is a
// no-op (found == false) when every analyzable link already has a pass.
func (m *FactsMachine) ProcessOnce(ctx context.Context) error {
	id, readable, found, err := m.nextUnprocessedLink(ctx)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}

	log.Printf("analyzing link id=%d (%d chars)", id, len(readable))

	result, aerr := m.analyzer.Analyze(ctx, readable)
	if aerr != nil {
		log.Printf("analysis failed for link id=%d: %v; recording error", id, aerr)
		return m.passes.SetPassError(id, aerr.Error())
	}

	if _, err := m.passes.UpsertPass(id, HashContent(readable), result); err != nil {
		return err
	}

	log.Printf("analyzed link id=%d: result=%d bytes", id, len(result))
	return nil
}
