package main

import (
	"database/sql"

	sq "github.com/bokwoon95/sq"
)

// LINK_QUEUE describes the link_queue table.
type LINK_QUEUE struct {
	sq.TableStruct
	ID           sq.NumberField
	URL          sq.StringField
	Content      sq.BinaryField `sq:"content"`
	ReadableText sq.StringField `sq:"readable_text"`
	LastScrappedAt sq.TimeField `sq:"last_scrapped_at"`
	AddedAt      sq.TimeField   `sq:"added_at"`
	Error        sq.StringField `sq:"error"`
}

var LQ = sq.New[LINK_QUEUE]("lq")

// LinkQueue is the Go model for a row in link_queue.
type LinkQueue struct {
	ID           int
	URL          string
	Content      string
	ReadableText string
	LastScrappedAt sql.NullTime
	AddedAt      sql.NullTime
	Error        sql.NullString
}

// Mapper scans a row from the link_queue table into a LinkQueue.
func Mapper(row *sq.Row) LinkQueue {
	var l LinkQueue
	l.ID = row.IntField(LQ.ID)
	l.URL = row.StringField(LQ.URL)
	l.Content = unpackZip(row.BytesField(LQ.Content))
	l.ReadableText = row.StringField(LQ.ReadableText)
	l.LastScrappedAt = row.NullTimeField(LQ.LastScrappedAt)
	l.AddedAt = row.NullTimeField(LQ.AddedAt)
	l.Error = row.NullStringField(LQ.Error)
	return l
}
