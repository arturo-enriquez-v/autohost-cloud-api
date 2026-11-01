# Etapa 1: build (soporta cross-compilation multi-arch amd64 / arm64)
FROM --platform=$BUILDPLATFORM golang:1.25 AS builder
ARG TARGETOS=linux
ARG TARGETARCH
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -ldflags="-w -s" -o autohost-cloud-api ./cmd/api && \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -ldflags="-w -s" -o autohost-migrate ./cmd/migrate

# Etapa 2: runtime
FROM gcr.io/distroless/base-debian12
WORKDIR /app
COPY --from=builder /app/autohost-cloud-api /usr/local/bin/autohost-cloud-api
COPY --from=builder /app/autohost-migrate /usr/local/bin/autohost-migrate
COPY --from=builder /app/migrations /app/migrations
LABEL org.opencontainers.image.licenses="AGPL-3.0-only"
EXPOSE 8080 9090

# USER 65532 es el usuario 'nonroot' preconfigurado por Google en distroless
USER 65532:65532

ENTRYPOINT ["/usr/local/bin/autohost-cloud-api"]
