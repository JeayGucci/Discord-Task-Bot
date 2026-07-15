.PHONY: fmt test vet build check run db-up db-down

fmt:
	gofmt -w $$(find . -name '*.go' -not -path './vendor/*')

test:
	go test -race ./...

vet:
	go vet ./...

build:
	go build -o bin/taskbot ./cmd/taskbot

check: fmt vet test build

run:
	go run ./cmd/taskbot

db-up:
	docker compose up -d postgres

db-down:
	docker compose down
