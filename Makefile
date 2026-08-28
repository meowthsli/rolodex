# Build flags. sqlite_fts5 enables fuzzy entity matching in the reconcile tool.
TAGS := sqlite_fts5
BIN  := bin

.PHONY: all build rolodex add-link add-content reconcile clear-events test vet clean

# Build everything.
all: build

build: rolodex add-link add-content reconcile clear-events

# Main rolodex service (scraper + facts machine).
rolodex:
	go build -tags $(TAGS) -o $(BIN)/rolodex .

# CLI tools.
add-link:
	go build -tags $(TAGS) -o $(BIN)/add-link ./cmd/add-link

add-content:
	go build -tags $(TAGS) -o $(BIN)/add-content ./cmd/add-content

reconcile:
	go build -tags $(TAGS) -o $(BIN)/reconcile ./cmd/reconcile

clear-events:
	go build -tags $(TAGS) -o $(BIN)/clear-events ./cmd/clear-events

# Run the full test suite (FTS5 enabled).
test:
	go test -tags $(TAGS) ./...

vet:
	go vet -tags $(TAGS) ./...

clean:
	rm -rf $(BIN)
