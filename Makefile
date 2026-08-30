# Build flags. sqlite_fts5 enables fuzzy entity matching in the reconcile tool.
TAGS := sqlite_fts5
BIN  := bin

.PHONY: all build rolodex add-link add-content reconcile clear-events merge-entities build-profiles profiles dashboard start test vet clean install-dashboard
# Build everything.
all: build

build: rolodex add-link add-content reconcile clear-events merge-entities build-profiles

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

merge-entities:
	go build -tags $(TAGS) -o $(BIN)/merge-entities ./cmd/merge-entities

build-profiles:
	go build -tags $(TAGS) -o $(BIN)/build-profiles ./cmd/build-profiles

# Rebuild every entity profile from the current knowledge graph.
profiles: build-profiles
	$(BIN)/build-profiles

# Run the full test suite (FTS5 enabled).
test:
	go test -tags $(TAGS) ./...

# Install dashboard deps into ./venv and launch the read-only Streamlit view.
install-dashboard:
	./venv/bin/pip install -r dashboard/requirements.txt

dashboard: start

start:
	./venv/bin/streamlit run dashboard/app.py

vet:
	go vet -tags $(TAGS) ./...

clean:
	rm -rf $(BIN)
