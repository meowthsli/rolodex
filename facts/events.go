package facts

import (
	"context"
	"database/sql"
	"encoding/json"
	"log"
	"time"

	"maragu.dev/goqite"
)

// entityQueue is the goqite queue name used for entity lifecycle events. The
// producer (entity create/merge) and the consumer (event handler) share it via
// the same database and name.
const entityQueue = "entities"

// EntityEvent is the payload published whenever an entity is created or updated
// (merged). It carries the canonical id and display name so a consumer can react
// without re-reading the database.
type EntityEvent struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

// EntityEventPublisher publishes entity lifecycle events. A no-op default keeps
// the repository usable without a queue (e.g. in tests).
type EntityEventPublisher interface {
	PublishEntityEvent(id int, name string) error
}

// GoqiteEntityPublisher publishes EntityEvents to a goqite queue backed by the
// shared database. The goqite schema (the "goqite" table) must already exist.
type GoqiteEntityPublisher struct {
	q *goqite.Queue
}

// NewGoqiteEntityPublisher creates a publisher for the entity queue. It panics
// only if db is nil (same contract as goqite.New); the schema is assumed present
// because migrations run at app start.
func NewGoqiteEntityPublisher(db *sql.DB) *GoqiteEntityPublisher {
	return &GoqiteEntityPublisher{q: goqite.New(goqite.NewOpts{DB: db, Name: entityQueue})}
}

func (p *GoqiteEntityPublisher) PublishEntityEvent(id int, name string) error {
	body, err := json.Marshal(EntityEvent{ID: id, Name: name})
	if err != nil {
		return err
	}
	return p.q.Send(context.Background(), goqite.Message{Body: body})
}

// NoopEntityPublisher discards events. It is the default so repositories work
// without a configured queue.
type NoopEntityPublisher struct{}

func (NoopEntityPublisher) PublishEntityEvent(int, string) error { return nil }

// StartEntityEventHandler runs a receive loop for the entity queue, invoking
// handler for every EntityEvent and deleting the message afterwards. It blocks
// until ctx is cancelled, then returns.
func StartEntityEventHandler(ctx context.Context, db *sql.DB, handler func(EntityEvent)) {
	q := goqite.New(goqite.NewOpts{DB: db, Name: entityQueue})
	for {
		m, err := q.ReceiveAndWait(ctx, time.Second)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("entity event handler receive: %v", err)
			continue
		}
		var ev EntityEvent
		if err := json.Unmarshal(m.Body, &ev); err != nil {
			log.Printf("entity event handler decode: %v", err)
		} else {
			handler(ev)
		}
		if err := q.Delete(ctx, m.ID); err != nil {
			log.Printf("entity event handler delete: %v", err)
		}
	}
}
