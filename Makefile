.PHONY: proto build test docker dev migrate-up migrate-down migrate-version

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

dev:
	docker compose up -d
	air -c .air.toml


migrate-up:
	go run ./cmd/migrate up

migrate-down:
	go run ./cmd/migrate down 1

migrate-version:
	go run ./cmd/migrate version
