// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package app

import (
	"context"
	"fmt"
	"log"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"
	"go.uber.org/fx"
)

// Database pool defaults. Without limits database/sql opens connections without bound under load (until
// Postgres max_connections is exhausted) and a stuck query never times out.
const (
	defaultMaxOpenConns     = 25
	defaultMaxIdleConns     = 10
	defaultConnMaxLifetime  = 30 * time.Minute
	defaultConnMaxIdleTime  = 5 * time.Minute
	defaultStatementTimeout = 30 * time.Second
)

func ProvideDB(lc fx.Lifecycle) (*sqlx.DB, error) {
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		log.Fatal("DATABASE_URL not set")
	}

	dsn, err := withStatementTimeout(dbURL, envDuration("DB_STATEMENT_TIMEOUT", defaultStatementTimeout))
	if err != nil {
		return nil, err
	}

	db, err := sqlx.Connect("postgres", dsn)
	if err != nil {
		return nil, err
	}

	db.SetMaxOpenConns(envInt("DB_MAX_OPEN_CONNS", defaultMaxOpenConns))
	db.SetMaxIdleConns(envInt("DB_MAX_IDLE_CONNS", defaultMaxIdleConns))
	db.SetConnMaxLifetime(envDuration("DB_CONN_MAX_LIFETIME", defaultConnMaxLifetime))
	db.SetConnMaxIdleTime(envDuration("DB_CONN_MAX_IDLE_TIME", defaultConnMaxIdleTime))

	// Fx cerrará la DB automáticamente al apagar la app
	lc.Append(fx.Hook{
		OnStop: func(ctx context.Context) error {
			log.Println("Cerrando conexión a la base de datos...")
			return db.Close()
		},
	})

	return db, nil
}

// withStatementTimeout adds a server-side statement_timeout runtime parameter to a postgres:// URL (lib/pq
// forwards unknown URL parameters as runtime parameters), unless the URL already sets one. It bounds every
// query, including those still using context.Background(). A zero timeout leaves the URL unchanged.
func withStatementTimeout(dbURL string, timeout time.Duration) (string, error) {
	if timeout <= 0 {
		return dbURL, nil
	}
	if !strings.HasPrefix(dbURL, "postgres://") && !strings.HasPrefix(dbURL, "postgresql://") {
		// Key/value DSN ("host=... dbname=..."): append the parameter if absent.
		if strings.Contains(dbURL, "statement_timeout") {
			return dbURL, nil
		}
		return fmt.Sprintf("%s statement_timeout=%d", strings.TrimSpace(dbURL), timeout.Milliseconds()), nil
	}
	u, err := url.Parse(dbURL)
	if err != nil {
		return "", fmt.Errorf("invalid DATABASE_URL: %w", err)
	}
	q := u.Query()
	if q.Get("statement_timeout") == "" {
		q.Set("statement_timeout", strconv.FormatInt(timeout.Milliseconds(), 10))
		u.RawQuery = q.Encode()
	}
	return u.String(), nil
}

func envInt(key string, def int) int {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			return n
		}
		log.Printf("[DB] ignoring invalid %s=%q", key, v)
	}
	return def
}

func envDuration(key string, def time.Duration) time.Duration {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d >= 0 {
			return d
		}
		log.Printf("[DB] ignoring invalid %s=%q", key, v)
	}
	return def
}
