.PHONY: build fmt vet test race check integration
build:
	go build -o bin/atc-cf-bot ./cmd/atc-cf-bot
fmt:
	gofmt -w cmd internal migrations/embed.go prompts/prompts.go
vet:
	go vet ./...
test:
	go test ./...
race:
	go test -race ./...
check: vet test race
integration:
	./scripts/test-postgres.sh
