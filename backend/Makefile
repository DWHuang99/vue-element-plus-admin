.PHONY: build run test test-integration test-unit lint clean migrate-up migrate-down migrate-create

# Binary output
BINARY_NAME=bin/server
MIGRATIONS_DIR=db/migrations

# Build the server binary
build:
	go build -o $(BINARY_NAME) ./cmd/server/

# Run the server (loads .env if present)
run:
	go run ./cmd/server/

# Run all tests
test:
	go test ./... -v -count=1

# Run integration tests only (requires Docker for testcontainers)
test-integration:
	go test ./internal/database/ ./internal/server/ ./internal/health/ -v -count=1 -tags=integration

# Run unit tests only (no Docker required)
test-unit:
	go test ./internal/config/ ./internal/middleware/ ./internal/logging/ -v -count=1

# Run linter
lint:
	golangci-lint run ./...

# Vet code
vet:
	go vet ./...

# Remove build artifacts
clean:
	rm -f $(BINARY_NAME)
	rm -f internal/database/sqlc/*.go
	rm -f internal/iam/postgres/sqlc/*.go
	rm -f internal/organization/postgres/sqlc/*.go
	rm -f internal/adminbff/postgres/sqlc/*.go

# Create a new migration file
# Usage: make migrate-create NAME=add_users_table
migrate-create:
	migrate create -ext sql -dir $(MIGRATIONS_DIR) -seq $(NAME)

# Apply all pending migrations (requires DATABASE_URL)
migrate-up:
	migrate -path $(MIGRATIONS_DIR) -database "$(DATABASE_URL)" up

# Rollback last migration (requires DATABASE_URL)
migrate-down:
	migrate -path $(MIGRATIONS_DIR) -database "$(DATABASE_URL)" down 1

# Generate sqlc code: transitional global package (removed in the final phase,
# until then legacy auth/rbac still consume it) + the three owner packages.
sqlc-generate:
	sqlc generate
	sqlc generate -f db/iam/sqlc.yaml
	sqlc generate -f db/organization/sqlc.yaml
	sqlc generate -f db/adminbff/sqlc.yaml

# Download dependencies
deps:
	go mod download
	go mod tidy
