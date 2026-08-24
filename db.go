package main

import (
	"database/sql"
	"strings"

	sq "github.com/bokwoon95/sq"
)

// LINK_QUEUE describes the link_queue table.
type LINK_QUEUE struct {
	sq.TableStruct
	ID           sq.NumberField
	URL          sq.StringField
	Content      sq.StringField `sq:"content"`
	LastScrapped sq.TimeField   `sq:"last_scrapped"`
	Error        sq.StringField `sq:"error"`
}

var LQ = sq.New[LINK_QUEUE]("lq")

// LinkQueue is the Go model for a row in link_queue.
type LinkQueue struct {
	ID           int
	URL          string
	Content      string
	LastScrapped sql.NullTime
	Error        sql.NullString
}

// normalizeURL strips a leading http:// or https:// scheme prefix.
func normalizeURL(raw string) string {
	lower := strings.ToLower(raw)
	switch {
	case strings.HasPrefix(lower, "https://"):
		return raw[len("https://"):]
	case strings.HasPrefix(lower, "http://"):
		return raw[len("http://"):]
	}
	return raw
}

// Mapper scans a row from the link_queue table into a LinkQueue.
func Mapper(row *sq.Row) LinkQueue {
	var l LinkQueue
	l.ID = row.IntField(LQ.ID)
	l.URL = row.StringField(LQ.URL)
	l.Content = row.StringField(LQ.Content)
	l.LastScrapped = row.NullTimeField(LQ.LastScrapped)
	l.Error = row.NullStringField(LQ.Error)
	return l
}
