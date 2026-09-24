.PHONY: build run test test-integration vet fmt tidy dev-up dev-down migrate-up migrate-down

build:
	go build -o bin/frenixpay ./cmd/frenixpay

run: build
	./bin/frenixpay

test:
	go test ./...

# Amount-matching integration tests against a real Postgres — see docs.md#testing.
test-integration:
	TEST_POSTGRES_DSN="$${TEST_POSTGRES_DSN:-postgres://frenixpay:frenixpay@localhost:5432/frenixpay?sslmode=disable}" \
		go test ./internal/orders/... -v

vet:
	go vet ./...

fmt:
	gofmt -w .

tidy:
	go mod tidy

dev-up:
	docker compose up -d

dev-down:
	docker compose down

# Requires the golang-migrate CLI (https://github.com/golang-migrate/migrate/releases)
# for ad-hoc use; the frenixpay binary also applies migrations itself on
# every startup, so this is only needed for manual inspection/rollback.
migrate-up:
	migrate -path migrations -database "$$POSTGRES_DSN" up

migrate-down:
	migrate -path migrations -database "$$POSTGRES_DSN" down 1
