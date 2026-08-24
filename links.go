package main

import (
	"database/sql"
	"errors"
	"time"

	sq "github.com/bokwoon95/sq"
)

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

// NewLink is the link constructor. It normalizes the URL and only inserts a
// new row when no link with that URL already exists. If the link already
// exists, it returns the existing row together with ErrLinkExists so the
// caller knows it can skip this link.
func (r *LinksRepository) NewLink(rawURL string) (LinkQueue, error) {
	url := normalizeURL(rawURL)

	existing, err := sq.FetchOne(r.db, sq.SQLite.From(LQ).Where(LQ.URL.Eq(sq.Value(url))), Mapper)
	if err == nil {
		return existing, ErrLinkExists
	} else if !errors.Is(err, sql.ErrNoRows) {
		return LinkQueue{}, err
	}

	result, err := sq.Exec(r.db, sq.SQLite.InsertInto(LQ).Columns(LQ.URL).Values(url))
	if err != nil {
		return LinkQueue{}, err
	}

	link, err := sq.FetchOne(r.db, sq.SQLite.From(LQ).Where(LQ.ID.Eq(sq.Value(result.LastInsertId))), Mapper)
	if err != nil {
		return LinkQueue{}, err
	}
	return link, nil
}

// UpdateLink changes the URL of an existing link_queue row by id.
// The URL is stored without its scheme prefix.
func (r *LinksRepository) UpdateLink(id int, rawURL string) (LinkQueue, error) {
	url := normalizeURL(rawURL)
	_, err := sq.Exec(r.db, sq.SQLite.Update(LQ).Set(LQ.URL.Set(url)).Where(LQ.ID.Eq(sq.Value(id))))
	if err != nil {
		return LinkQueue{}, err
	}

	link, err := sq.FetchOne(r.db, sq.SQLite.From(LQ).Where(LQ.ID.Eq(sq.Value(id))), Mapper)
	if err != nil {
		return LinkQueue{}, err
	}
	return link, nil
}

// GetLink fetches a single link_queue row by id.
func (r *LinksRepository) GetLink(id int) (LinkQueue, error) {
	return sq.FetchOne(r.db, sq.SQLite.From(LQ).Where(LQ.ID.Eq(sq.Value(id))), Mapper)
}

// ListLinks returns all rows from link_queue.
func (r *LinksRepository) ListLinks() ([]LinkQueue, error) {
	return sq.FetchAll(r.db, sq.SQLite.From(LQ), Mapper)
}

// GetNextPendingLink returns a single link whose last_scrapped is NULL, or a
// zero-value LinkQueue if there are no pending links.
func (r *LinksRepository) GetNextPendingLink() (LinkQueue, error) {
	link, err := sq.FetchOne(r.db, sq.SQLite.From(LQ).Where(LQ.LastScrapped.IsNull()).Limit(1), Mapper)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return LinkQueue{}, nil
		}
		return LinkQueue{}, err
	}
	return link, nil
}

// SaveScrapeResult stores the fetched content (zipped) and its extracted
// readable text, and stamps last_scrapped, clearing any previous error.
func (r *LinksRepository) SaveScrapeResult(id int, content, readableText string) error {
	zipped, err := packZip(content)
	if err != nil {
		return err
	}
	_, err = sq.Exec(r.db, sq.SQLite.Update(LQ).
		Set(LQ.Content.Set(zipped)).
		Set(LQ.ReadableText.Set(readableText)).
		Set(LQ.Error.Set(sq.Expr("NULL"))).
		Set(LQ.LastScrapped.Set(sq.Value(time.Now()))).
		Where(LQ.ID.Eq(sq.Value(id))))
	return err
}

// SaveScrapeError records a failure for the given link and stamps
// last_scrapped so the link is not retried.
func (r *LinksRepository) SaveScrapeError(id int, errMsg string) error {
	_, err := sq.Exec(r.db, sq.SQLite.Update(LQ).
		Set(LQ.Error.Set(errMsg)).
		Set(LQ.LastScrapped.Set(sq.Value(time.Now()))).
		Where(LQ.ID.Eq(sq.Value(id))))
	return err
}
