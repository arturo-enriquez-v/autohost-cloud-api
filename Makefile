run:
	go run ./cmd/api

build:
	go build -o bin/autohost-cloud-lite ./cmd/api

test:
	go test ./...

docker:
	docker build -t autohost-cloud-lite .

run-air:
	docker compose up -d
	$(shell go env GOPATH)/bin/air -c .air.toml


migrate-up:
	go run ./cmd/migrate up

migrate-down:
	go run ./cmd/migrate down 1

migrate-version:
	go run ./cmd/migrate version
