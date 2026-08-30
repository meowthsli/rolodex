package facts

import (
	"context"
	"database/sql"
	"encoding/json"
	"log"
	"time"

	sq "github.com/bokwoon95/sq"
)

// Analyzer turns a single link's readable text into a structured analysis
// result. The returned string is stored verbatim as JSON in passes.result.
// Implementations may call external models, rule engines, etc. The prompt is the
// domain-specific system prompt chosen by the facts machine.
type Analyzer interface {
	Analyze(ctx context.Context, prompt, content string) (string, error)
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
func (m MockAnalyzer) Analyze(ctx context.Context, prompt, content string) (string, error) {
	return m.Result, m.Err
}

// FactsMachine periodically pulls the next link whose readable text has not yet
// been analyzed, runs it through an Analyzer, and persists the result as passes.
// A single link may carry several analysis domains; the machine creates one set
// of chunk passes per domain, using the domain's prompt from the prompts map. A
// domain with no registered prompt is skipped. Analyzed entities are reconciled
// into the canonical entities table.
type FactsMachine struct {
	db       *sql.DB
	analyzer Analyzer
	prompts  map[string]string
	Chunker  *TextChunker
	passes   *PassesRepository
	entities *EntitiesRepository
	tick     time.Duration
	stop     chan struct{}
}

// NewFactsMachine builds a FactsMachine. prompts maps each analysis domain to
// the system prompt used when analyzing content for that domain. A non-positive
// tick defaults to 5s. The Chunker defaults to sentence/paragraph-aware
// splitting with the package's default size and overlap.
func NewFactsMachine(db *sql.DB, analyzer Analyzer, prompts map[string]string, tick time.Duration, p EntityEventPublisher) *FactsMachine {
	if tick <= 0 {
		tick = 5 * time.Second
	}
	return &FactsMachine{db: db, analyzer: analyzer, prompts: prompts, Chunker: NewTextChunker(),
		passes:   NewPassesRepository(db),
		entities: NewEntitiesRepository(db, p),
		tick:     tick}
}

// Start launches the analysis loop in a background goroutine.
func (m *FactsMachine) Start() {
	log.Printf("facts machine started (tick: %s, domains: %d)", m.tick, len(m.prompts))
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

// linkContent is the minimal projection nextUnprocessedLink reads from
// link_queue: just the id, the readable text and the domains array.
type linkContent struct {
	ID       int
	Readable string
	Domains  []string
}

// parseLinkDomains decodes a link's domains JSON array. A malformed or empty
// value yields the default single "venture" domain.
func parseLinkDomains(raw string) []string {
	if raw == "" {
		return []string{"venture"}
	}
	var d []string
	if err := json.Unmarshal([]byte(raw), &d); err != nil || len(d) == 0 {
		return []string{"venture"}
	}
	return d
}

// nextUnprocessedLink returns the id, readable text and domains of a link that
// has been scraped (last_scrapped_at set) and still has at least one configured
// domain, with a registered prompt, that has no pass yet. found is false when
// nothing is left to analyze. Domains without a registered prompt are ignored
// (they can never be processed), so they never keep a link pending.
func (m *FactsMachine) nextUnprocessedLink(ctx context.Context) (id int, readable string, domains []string, found bool, err error) {
	links, err := sq.FetchAllContext(ctx, m.db, sq.SQLite.Queryf(
		"SELECT lq.id AS lq_id, lq.readable_text AS lq_readable, lq.domains AS lq_domains FROM link_queue lq "+
			"WHERE lq.last_scrapped_at IS NOT NULL ORDER BY lq.id"),
		func(row *sq.Row) linkContent {
			return linkContent{
				ID:       row.Int("lq_id"),
				Readable: row.String("lq_readable"),
				Domains:  parseLinkDomains(row.String("lq_domains")),
			}
		})
	if err != nil {
		return 0, "", nil, false, err
	}
	for _, lc := range links {
		for _, d := range lc.Domains {
			// Skip domains with no registered prompt: they can never be
			// processed, so they must not keep the link pending.
			if _, ok := m.prompts[d]; !ok {
				continue
			}
			hasPass, err := m.passes.LinkDomainHasPass(lc.ID, d)
			if err != nil {
				return 0, "", nil, false, err
			}
			if !hasPass {
				return lc.ID, lc.Readable, lc.Domains, true, nil
			}
		}
	}
	return 0, "", nil, false, nil
}

// ProcessOnce analyzes a single unprocessed link, creating a set of chunk passes
// for each of its configured domains. It is a no-op (found == false) when every
// analyzable link has been processed for every prompted domain.
func (m *FactsMachine) ProcessOnce(ctx context.Context) error {
	id, readable, domains, found, err := m.nextUnprocessedLink(ctx)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}

	log.Printf("analyzing link id=%d (%d chars), domains=%v", id, len([]rune(readable)), domains)

	chunks := m.Chunker.Chunk(readable)
	for _, domain := range domains {
		prompt, ok := m.prompts[domain]
		if !ok {
			log.Printf("link id=%d: no prompt for domain %q; skipping", id, domain)
			continue
		}

		hasPass, err := m.passes.LinkDomainHasPass(id, domain)
		if err != nil {
			return err
		}
		if hasPass {
			log.Printf("link id=%d domain %q already analyzed; skipping", id, domain)
			continue
		}

		// Drop any previous chunk passes for this link/domain so a re-run fully
		// replaces them (the chunk count can change as the source grows/shrinks).
		if err := m.passes.DeletePassesByLinkDomain(id, domain); err != nil {
			return err
		}

		for _, ch := range chunks {
			result, aerr := m.analyzer.Analyze(ctx, prompt, ch.Text)
			if aerr != nil {
				log.Printf("analysis failed for link id=%d domain %q chunk %d: %v; recording error", id, domain, ch.Index, aerr)
				if err := m.passes.SetPassError(id, domain, ch.Index, ch.Start, ch.End, ch.Text, aerr.Error()); err != nil {
					return err
				}
				continue
			}
			pass, err := m.passes.UpsertPass(id, domain, ch.Index, ch.Start, ch.End, ch.Text, HashContent(ch.Text), result)
			if err != nil {
				return err
			}
			if err := m.entities.ExtractPass(ctx, pass); err != nil {
				log.Printf("entity extraction failed for link id=%d domain %q chunk %d: %v", id, domain, ch.Index, err)
			}
		}
	}

	log.Printf("done for link id=%d: %d chunks x %d domains", id, len(chunks), len(domains))
	return nil
}
