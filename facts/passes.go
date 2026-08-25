package facts

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"time"

	sq "github.com/bokwoon95/sq"
)

// PASSES describes the passes table: one analysis record per scraped link.
type PASSES struct {
	sq.TableStruct
	ID           sq.NumberField
	LinkQueueID  sq.NumberField `sq:"link_queue_id"`
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

// UpsertPass records (or refreshes) the analysis result for a link. Because
// there is exactly one pass per link, a conflicting link_queue_id overwrites
// the previous result and bumps created_at, effectively re-running the pass.
func (r *PassesRepository) UpsertPass(linkQueueID int, contentHash, result string) (Pass, error) {
	_, err := sq.Exec(r.db, sq.SQLite.Queryf(
		"INSERT INTO passes (link_queue_id, content_hash, result) VALUES ({}, {}, {}) "+
			"ON CONFLICT(link_queue_id) DO UPDATE SET "+
			"content_hash = excluded.content_hash, result = excluded.result, "+
			"created_at = CURRENT_TIMESTAMP, error = NULL",
		linkQueueID, contentHash, result))
	if err != nil {
		return Pass{}, err
	}
	return r.GetPassByLink(linkQueueID)
}

// GetPass fetches a single pass by its id.
func (r *PassesRepository) GetPass(id int) (Pass, error) {
	return sq.FetchOne(r.db, sq.SQLite.Queryf(
		"SELECT {*} FROM passes WHERE id = {}", id), PassMapper)
}

// GetPassByLink fetches the pass for a given link_queue row.
func (r *PassesRepository) GetPassByLink(linkQueueID int) (Pass, error) {
	return sq.FetchOne(r.db, sq.SQLite.Queryf(
		"SELECT {*} FROM passes WHERE link_queue_id = {}", linkQueueID), PassMapper)
}

// SetPassError records a failure for the pass of the given link, so callers
// can distinguish "not yet analyzed" (no row) from "analysis failed".
func (r *PassesRepository) SetPassError(linkQueueID int, errMsg string) error {
	_, err := sq.Exec(r.db, sq.SQLite.Queryf(
		"INSERT INTO passes (link_queue_id, content_hash, result, error) VALUES ({}, '', '', {}) "+
			"ON CONFLICT(link_queue_id) DO UPDATE SET error = excluded.error, created_at = CURRENT_TIMESTAMP",
		linkQueueID, errMsg))
	return err
}
