# Etapa 1: build
FROM golang:1.25 AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -ldflags="-w -s" -o autohost-cloud-api ./cmd/api

# Etapa 2: runtime
FROM gcr.io/distroless/base-debian12
COPY --from=builder /app/autohost-cloud-api /usr/local/bin/autohost-cloud-api
EXPOSE 8080

# USER 65532 es el usuario 'nonroot' preconfigurado por Google en distroless
USER 65532:65532

ENTRYPOINT ["/usr/local/bin/autohost-cloud-api"]