package facts

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"time"

	sq "github.com/bokwoon95/sq"
)

// PASSES describes the passes table: one analysis record per (link, domain,
// chunk). A long readable text is split into overlapping chunks, each analyzed
// independently and stored as its own row.
type PASSES struct {
	sq.TableStruct
	ID           sq.NumberField
	LinkQueueID  sq.NumberField `sq:"link_queue_id"`
	Domain       sq.StringField `sq:"domain"`
	ChunkIndex   sq.NumberField `sq:"chunk_index"`
	ChunkStart   sq.NumberField `sq:"chunk_start"`
	ChunkEnd     sq.NumberField `sq:"chunk_end"`
	ChunkText    sq.StringField `sq:"chunk_text"` // the chunk text analyzed by this pass
	ContentHash  sq.StringField `sq:"content_hash"`
	Result       sq.StringField `sq:"result"`
	CreatedAt    sq.TimeField   `sq:"created_at"`
	Error        sq.StringField `sq:"error"`
}

var PS = sq.New[PASSES]("p")

// Pass is the Go model for a row in passes.
type Pass struct {
	ID           int
	LinkQueueID  int
	Domain       string
	ChunkIndex   int
	ChunkStart   int
	ChunkEnd     int
	ChunkText    string // the chunk text analyzed by this pass
	ContentHash  string
	Result       string
	CreatedAt    time.Time
	Error        sql.NullString
}

// PassMapper scans a row from the passes table into a Pass.
func PassMapper(row *sq.Row) Pass {
	var p Pass
	p.ID = row.Int("id")
	p.LinkQueueID = row.Int("link_queue_id")
	p.Domain = row.String("domain")
	p.ChunkIndex = row.Int("chunk_index")
	p.ChunkStart = row.Int("chunk_start")
	p.ChunkEnd = row.Int("chunk_end")
	p.ChunkText = row.String("chunk_text")
	p.ContentHash = row.String("content_hash")
	p.Result = row.String("result")
	p.CreatedAt = row.Time("created_at")
	p.Error = row.NullString("error")
	return p
}

// HashContent returns a stable SHA-256 hex digest of the text a pass analyzes.
// Storing this lets a later pass detect that the source content drifted and
// that previously extracted excerpts/offsets are no longer valid.
func HashContent(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// PassesRepository provides passes table operations.
type PassesRepository struct {
	db *sql.DB
}

// NewPassesRepository creates a repository backed by the given database.
func NewPassesRepository(db *sql.DB) *PassesRepository {
	return &PassesRepository{db: db}
}

// UpsertPass records (or refreshes) the analysis result for a single chunk of a
// link within a domain. There is exactly one pass per (link, domain, chunk),
// so a conflicting triple overwrites the previous result and bumps created_at,
// effectively re-running that chunk's pass.
func (r *PassesRepository) UpsertPass(linkQueueID int, domain string, chunkIndex, chunkStart, chunkEnd int, chunkText, contentHash, result string) (Pass, error) {
	_, err := sq.Exec(r.db, sq.SQLite.Queryf(
		"INSERT INTO passes (link_queue_id, domain, chunk_index, chunk_start, chunk_end, chunk_text, content_hash, result) "+
			"VALUES ({}, {}, {}, {}, {}, {}, {}, {}) "+
			"ON CONFLICT(link_queue_id, domain, chunk_index) DO UPDATE SET "+
			"content_hash = excluded.content_hash, result = excluded.result, "+
			"chunk_text = excluded.chunk_text, chunk_start = excluded.chunk_start, chunk_end = excluded.chunk_end, "+
			"created_at = CURRENT_TIMESTAMP, error = NULL",
		linkQueueID, domain, chunkIndex, chunkStart, chunkEnd, chunkText, contentHash, result))
	if err != nil {
		return Pass{}, err
	}
	return r.GetPassByLink(linkQueueID, domain, chunkIndex)
}

// GetPass fetches a single pass by its id.
func (r *PassesRepository) GetPass(id int) (Pass, error) {
	return sq.FetchOne(r.db, sq.SQLite.Queryf(
		"SELECT {*} FROM passes WHERE id = {}", id), PassMapper)
}

// GetPassByLink fetches the pass for a given link/domain/chunk triple.
func (r *PassesRepository) GetPassByLink(linkQueueID int, domain string, chunkIndex int) (Pass, error) {
	return sq.FetchOne(r.db, sq.SQLite.Queryf(
		"SELECT {*} FROM passes WHERE link_queue_id = {} AND domain = {} AND chunk_index = {}",
		linkQueueID, domain, chunkIndex), PassMapper)
}

// ListPassesByLink returns every chunk pass for a link within a domain, ordered
// by chunk index.
func (r *PassesRepository) ListPassesByLink(linkQueueID int, domain string) ([]Pass, error) {
	return sq.FetchAll(r.db, sq.SQLite.Queryf(
		"SELECT {*} FROM passes WHERE link_queue_id = {} AND domain = {} ORDER BY chunk_index",
		linkQueueID, domain), PassMapper)
}

// DeletePassesByLinkDomain removes all chunk passes for a link within a domain,
// used before re-analyzing so the new set of chunks fully replaces the old one.
func (r *PassesRepository) DeletePassesByLinkDomain(linkQueueID int, domain string) error {
	_, err := sq.Exec(r.db, sq.SQLite.Queryf(
		"DELETE FROM passes WHERE link_queue_id = {} AND domain = {}", linkQueueID, domain))
	return err
}

// SetPassError records a failure for one chunk of a link/domain, so callers can
// distinguish "not yet analyzed" (no row) from "analysis failed".
func (r *PassesRepository) SetPassError(linkQueueID int, domain string, chunkIndex, chunkStart, chunkEnd int, chunkText, errMsg string) error {
	_, err := sq.Exec(r.db, sq.SQLite.Queryf(
		"INSERT INTO passes (link_queue_id, domain, chunk_index, chunk_start, chunk_end, chunk_text, content_hash, result, error) "+
			"VALUES ({}, {}, {}, {}, {}, {}, '', '', {}) "+
			"ON CONFLICT(link_queue_id, domain, chunk_index) DO UPDATE SET error = excluded.error, created_at = CURRENT_TIMESTAMP",
		linkQueueID, domain, chunkIndex, chunkStart, chunkEnd, chunkText, errMsg))
	return err
}
