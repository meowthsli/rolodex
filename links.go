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

// AddLink inserts a new row into link_queue and returns the saved link.
func (r *LinksRepository) AddLink(url string) (LinkQueue, error) {
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
func (r *LinksRepository) UpdateLink(id int, url string) (LinkQueue, error) {
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

// SaveScrapeResult stores the fetched content and stamps last_scrapped.
func (r *LinksRepository) SaveScrapeResult(id int, content string) error {
	_, err := sq.Exec(r.db, sq.SQLite.Update(LQ).
		Set(LQ.Content.Set(content)).
		Set(LQ.LastScrapped.Set(sq.Value(time.Now()))).
		Where(LQ.ID.Eq(sq.Value(id))))
	return err
}
