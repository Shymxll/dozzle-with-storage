.PHONY: test vet build integration-test

test:
	go test ./...

vet:
	go vet ./...

build:
	go build ./cmd/log-archive

integration-test:
	go test -run TestMillionRowsQueryLatency -timeout 15m ./internal/storage
