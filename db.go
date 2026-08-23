package main

import (
	sq "github.com/bokwoon95/sq"
)

// LINK_QUEUE describes the link_queue table.
type LINK_QUEUE struct {
	sq.TableStruct
	ID  sq.NumberField
	URL sq.StringField
}

var LQ = sq.New[LINK_QUEUE]("")

// LinkQueue is the Go model for a row in link_queue.
type LinkQueue struct {
	ID  int
	URL string
}

// Mapper scans a row from the link_queue table into a LinkQueue.
func Mapper(row *sq.Row) LinkQueue {
	var l LinkQueue
	l.ID = row.IntField(LQ.ID)
	l.URL = row.StringField(LQ.URL)
	return l
}
