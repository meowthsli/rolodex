package facts

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"

	sq "github.com/bokwoon95/sq"
)

// EXCERPTS describes the excerpts table: proof spans produced by a pass. Each
// span anchors (proves) facts that a later subsystem will extract. Offsets are
// relative to the readable_text version identified by the parent pass's
// content_hash, so they are only valid while that hash matches.
type EXCERPTS struct {
	sq.TableStruct
	ID          sq.NumberField
	PassID      sq.NumberField `sq:"pass_id"`
	Text        sq.StringField `sq:"text"`
	StartOffset sq.NumberField `sq:"start_offset"`
	EndOffset   sq.NumberField `sq:"end_offset"`
	SpanHash    sq.StringField `sq:"span_hash"`
}

var EX = sq.New[EXCERPTS]("e")

// Excerpt is the Go model for a row in excerpts.
type Excerpt struct {
	ID          int
	PassID      int
	Text        string
	StartOffset int
	EndOffset   int
	SpanHash    sql.NullString
}

// ExcerptMapper scans a row from the excerpts table into an Excerpt.
func ExcerptMapper(row *sq.Row) Excerpt {
	var e Excerpt
	e.ID = row.Int("id")
	e.PassID = row.Int("pass_id")
	e.Text = row.String("text")
	e.StartOffset = row.Int("start_offset")
	e.EndOffset = row.Int("end_offset")
	e.SpanHash = row.NullString("span_hash")
	return e
}

// HashSpan returns a stable SHA-256 hex digest of a span's text, used to
// de-duplicate identical excerpts and to look spans up across passes.
func HashSpan(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// ExcerptsRepository provides excerpts table operations.
type ExcerptsRepository struct {
	db *sql.DB
}

// NewExcerptsRepository creates a repository backed by the given database.
func NewExcerptsRepository(db *sql.DB) *ExcerptsRepository {
	return &ExcerptsRepository{db: db}
}

// SaveExcerpt stores a proof span for a pass. Identical spans (same pass and
// offsets) are de-duplicated via INSERT OR IGNORE; the canonical Excerpt row
// (whether newly inserted or already present) is returned.
func (r *ExcerptsRepository) SaveExcerpt(passID int, text string, startOffset, endOffset int, spanHash string) (Excerpt, error) {
	_, err := sq.Exec(r.db, sq.SQLite.Queryf(
		"INSERT OR IGNORE INTO excerpts (pass_id, text, start_offset, end_offset, span_hash) "+
			"VALUES ({}, {}, {}, {}, {})",
		passID, text, startOffset, endOffset, spanHash))
	if err != nil {
		return Excerpt{}, err
	}
	return sq.FetchOne(r.db, sq.SQLite.Queryf(
		"SELECT {*} FROM excerpts WHERE pass_id = {} AND start_offset = {} AND end_offset = {}",
		passID, startOffset, endOffset), ExcerptMapper)
}

// ListExcerptsByPass returns all excerpts belonging to a pass.
func (r *ExcerptsRepository) ListExcerptsByPass(passID int) ([]Excerpt, error) {
	return sq.FetchAll(r.db, sq.SQLite.Queryf(
		"SELECT {*} FROM excerpts WHERE pass_id = {}", passID), ExcerptMapper)
}

// ListExcerptsByLink returns all excerpts for a link, reached by joining
// through its pass (excerpts have no direct link_queue_id by design).
func (r *ExcerptsRepository) ListExcerptsByLink(linkQueueID int) ([]Excerpt, error) {
	return sq.FetchAll(r.db, sq.SQLite.Queryf(
		"SELECT e.id, e.pass_id, e.text, e.start_offset, e.end_offset, e.span_hash "+
			"FROM excerpts e JOIN passes p ON p.id = e.pass_id WHERE p.link_queue_id = {}",
		linkQueueID), ExcerptMapper)
}
