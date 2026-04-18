.PHONY: proto build test docker dev run-air migrate-up migrate-down migrate-version migrate-force

proto:
	protoc --go_out=. --go_opt=module=github.com/arturo/autohost-cloud-api \
		--go-grpc_out=. --go-grpc_opt=module=github.com/arturo/autohost-cloud-api \
		proto/node_agent/v1/node_agent.proto

build:
	go build -o bin/autohost-cloud-lite ./cmd/api

test:
	go test ./...

docker:
	docker build -t autohost-cloud-lite .

dev: run-air

run-air:
	docker compose up -d
	$(shell go env GOPATH)/bin/air -c .air.toml


migrate-up:
	go run ./cmd/migrate up

migrate-down:
	go run ./cmd/migrate down 1

migrate-version:
	go run ./cmd/migrate version

migrate-force:
	@if [ -z "$(VERSION)" ]; then echo "Usage: make migrate-force VERSION=<number>"; exit 1; fi
	go run ./cmd/migrate force $(VERSION)
