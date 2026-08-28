package grab

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	sq "github.com/bokwoon95/sq"
)

// LINK_QUEUE describes the link_queue table.
type LINK_QUEUE struct {
	sq.TableStruct
	ID            sq.NumberField
	URL           sq.StringField
	Content       sq.BinaryField `sq:"content"`
	ReadableText  sq.StringField `sq:"readable_text"`
	LastScrappedAt sq.TimeField  `sq:"last_scrapped_at"`
	AddedAt       sq.TimeField   `sq:"added_at"`
	Error         sq.StringField `sq:"error"`
	Generation    sq.NumberField `sq:"generation"`
}

var LQ = sq.New[LINK_QUEUE]("lq")

// LinkQueue is the Go model for a row in link_queue.
type LinkQueue struct {
	ID            int
	URL           string
	Content       string
	ReadableText  string
	LastScrappedAt sql.NullTime
	AddedAt       sql.NullTime
	Error         sql.NullString
	Generation    int
}

// Mapper scans a row from the link_queue table into a LinkQueue.
func Mapper(row *sq.Row) LinkQueue {
	var l LinkQueue
	l.ID = row.Int("id")
	l.URL = row.String("url")
	l.Content = unpackZip(row.Bytes("content"))
	l.ReadableText = row.String("readable_text")
	l.LastScrappedAt = row.NullTime("last_scrapped_at")
	l.AddedAt = row.NullTime("added_at")
	l.Error = row.NullString("error")
	l.Generation = row.Int("generation")
	return l
}

// LinksRepository aggregates the database handle and provides link_queue operations.
type LinksRepository struct {
	db *sql.DB
}

// NewLinksRepository creates a repository backed by the given database.
func NewLinksRepository(db *sql.DB) *LinksRepository {
	return &LinksRepository{db: db}
}

// ErrLinkExists is returned by NewLink when a link with the same normalized
// URL is already present in the queue.
var ErrLinkExists = errors.New("link already exists")

// NewLink is the link constructor. It normalizes the URL, stamps the given
// generation and only inserts a new row when no link with that URL already
// exists. If the link already exists, it re-queues it by stamping added_at with
// the current time (so the scraper re-fetches it if the stored content predates
// this rediscovery) and returns the existing row together with ErrLinkExists so
// the caller knows it can skip inserting a duplicate row. The existing row's
// generation is preserved on rediscovery.
func (r *LinksRepository) NewLink(rawURL string, generation int) (LinkQueue, error) {
	url := normalizeURL(rawURL)

	existing, err := sq.FetchOne(r.db, sq.SQLite.Queryf(
		"SELECT {*} FROM link_queue WHERE url = {}", url), Mapper)
	if err == nil {
		// Rediscovery: bump added_at so the scraper re-fetches this link if
		// its stored content is older than when we found it again.
		_, uerr := sq.Exec(r.db, sq.SQLite.Queryf(
			"UPDATE link_queue SET added_at = {} WHERE id = {}", time.Now(), existing.ID))
		if uerr != nil {
			return LinkQueue{}, uerr
		}
		return existing, ErrLinkExists
	} else if !errors.Is(err, sql.ErrNoRows) {
		return LinkQueue{}, err
	}

	_, err = sq.Exec(r.db, sq.SQLite.Queryf(
		"INSERT INTO link_queue (url, generation, added_at) VALUES ({}, {}, {})", url, generation, time.Now()))
	if err != nil {
		return LinkQueue{}, err
	}

	link, err := sq.FetchOne(r.db, sq.SQLite.Queryf(
		"SELECT {*} FROM link_queue WHERE id = last_insert_rowid()"), Mapper)
	if err != nil {
		return LinkQueue{}, err
	}
	return link, nil
}

// UpdateLink changes the URL of an existing link_queue row by id.
// The URL is stored without its scheme prefix.
func (r *LinksRepository) UpdateLink(id int, rawURL string) (LinkQueue, error) {
	url := normalizeURL(rawURL)
	_, err := sq.Exec(r.db, sq.SQLite.Queryf(
		"UPDATE link_queue SET url = {} WHERE id = {}", url, id))
	if err != nil {
		return LinkQueue{}, err
	}

	link, err := sq.FetchOne(r.db, sq.SQLite.Queryf(
		"SELECT {*} FROM link_queue WHERE id = {}", id), Mapper)
	if err != nil {
		return LinkQueue{}, err
	}
	return link, nil
}

// GetLink fetches a single link_queue row by id.
func (r *LinksRepository) GetLink(id int) (LinkQueue, error) {
	return sq.FetchOne(r.db, sq.SQLite.Queryf(
		"SELECT {*} FROM link_queue WHERE id = {}", id), Mapper)
}

// ListLinks returns all rows from link_queue.
func (r *LinksRepository) ListLinks() ([]LinkQueue, error) {
	return sq.FetchAll(r.db, sq.SQLite.Queryf("SELECT {*} FROM link_queue"), Mapper)
}

// GetNextPendingLink returns a single link that still needs scraping, or a
// zero-value LinkQueue if there are no pending links. A link is pending when
// it has never been scraped (last_scrapped_at IS NULL) or when its stored
// content is older than the moment the link was added/re-queued
// (last_scrapped_at < added_at), indicating the content should be refreshed.
func (r *LinksRepository) GetNextPendingLink() (LinkQueue, error) {
	link, err := sq.FetchOne(r.db, sq.SQLite.Queryf(
		"SELECT {*} FROM link_queue WHERE last_scrapped_at IS NULL OR last_scrapped_at < added_at LIMIT 1"), Mapper)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return LinkQueue{}, nil
		}
		return LinkQueue{}, err
	}
	return link, nil
}

// ErrEmptyReadable is returned by SaveScrapeResult when the extracted readable
// text is empty, guaranteeing a link is never marked as scraped (last_scrapped_at
// set) without having content to analyze.
var ErrEmptyReadable = errors.New("readable_text must not be empty")

// SaveScrapeResult stores the fetched content (zipped) and its extracted
// readable text, and stamps last_scrapped_at, clearing any previous error.
// It refuses to record a result with empty readable_text: such a row would be
// picked up by the analysis pipeline (which keys off last_scrapped_at) yet have
// nothing to analyze, so we leave the link un-scraped for retry instead.
func (r *LinksRepository) SaveScrapeResult(id int, content, readableText string) error {
	if readableText == "" {
		return fmt.Errorf("link %d: %w", id, ErrEmptyReadable)
	}
	zipped, err := packZip(content)
	if err != nil {
		return err
	}
	_, err = sq.Exec(r.db, sq.SQLite.Queryf(
		"UPDATE link_queue SET content = {}, readable_text = {}, error = NULL, last_scrapped_at = {} WHERE id = {}",
		zipped, readableText, time.Now(), id))
	return err
}

// DeleteLink removes a link_queue row entirely by id. Used to drop links that
// are blacklisted before any content is fetched or stored.
func (r *LinksRepository) DeleteLink(id int) error {
	_, err := sq.Exec(r.db, sq.SQLite.Queryf(
		"DELETE FROM link_queue WHERE id = {}", id))
	return err
}

// SaveScrapeError records a failure for the given link and stamps
// last_scrapped_at so the link is not retried.
func (r *LinksRepository) SaveScrapeError(id int, errMsg string) error {
	_, err := sq.Exec(r.db, sq.SQLite.Queryf(
		"UPDATE link_queue SET error = {}, last_scrapped_at = {} WHERE id = {}",
		errMsg, time.Now(), id))
	return err
}
