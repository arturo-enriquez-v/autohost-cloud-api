// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package postgres

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	_ "github.com/lib/pq"

	"github.com/arturo/autohost-cloud-api/internal/core/connectors"
)

// TestConnectorRepository runs against a real Postgres (AUTOHOST_DB_INTEGRATION=1 + DATABASE_URL) with a
// throwaway user and project that are deleted afterwards (cascading to connectors and runs).
func TestConnectorRepository(t *testing.T) {
	if os.Getenv("AUTOHOST_DB_INTEGRATION") != "1" || os.Getenv("DATABASE_URL") == "" {
		t.Skip("set AUTOHOST_DB_INTEGRATION=1 and DATABASE_URL to run")
	}
	db, err := sqlx.Connect("postgres", os.Getenv("DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()

	var userID, projectID string
	if err := db.Get(&userID, `INSERT INTO users (email, name) VALUES ('connectors-repo@autohost.invalid', 'test') RETURNING id`); err != nil {
		t.Fatal(err)
	}
	defer db.Exec(`DELETE FROM users WHERE id = $1`, userID)
	if err := db.Get(&projectID, `INSERT INTO projects (user_id, name) VALUES ($1, 'connectors-test') RETURNING id`, userID); err != nil {
		t.Fatal(err)
	}

	repo := NewConnectorRepository(db)
	c := &connectors.Connector{
		ID: uuid.NewString(),
		Definition: connectors.Definition{
			ProjectID: projectID, Name: "Status", Kind: connectors.KindQuery, Method: "GET",
			URL:      "https://api.example.com/{{id}}",
			Headers:  []connectors.KeyValue{{Name: "X-A", Value: "b"}},
			Params:   []connectors.Param{{Name: "id", Type: "string", Required: true}},
			Auth:     connectors.Auth{Type: connectors.AuthBearer},
			Response: connectors.ResponseMapping{Root: "items", Fields: []connectors.FieldMapping{{Label: "N", Path: "n"}}, View: "table"},
			BodyType: "none", CacheTTLSeconds: 30, TimeoutSeconds: 10,
		},
		SecretCiphertext: "v1:abc",
		CreatedBy:        userID,
	}
	created, err := repo.Create(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	if !created.HasSecret || created.Params[0].Name != "id" || created.Response.Fields[0].Path != "n" || created.CreatedBy != userID {
		t.Fatalf("created = %+v", created)
	}
	dup := *c
	dup.ID = uuid.NewString()
	if _, err := repo.Create(ctx, &dup); !errors.Is(err, connectors.ErrNameExists) {
		t.Fatalf("duplicate name: err = %v", err)
	}

	created.Name, created.SecretCiphertext = "Status 2", ""
	updated, err := repo.Update(ctx, created)
	if err != nil || updated.Name != "Status 2" || updated.HasSecret || !updated.UpdatedAt.After(created.CreatedAt.Add(-1)) {
		t.Fatalf("update: %+v, %v", updated, err)
	}

	list, err := repo.ListByProjects(ctx, []string{projectID, uuid.NewString()})
	if err != nil || len(list) != 1 {
		t.Fatalf("list: %d, %v", len(list), err)
	}

	for i := 0; i < 5; i++ {
		if err := repo.InsertRun(ctx, &connectors.Run{ConnectorID: c.ID, UserID: userID, Params: map[string]string{"i": fmt.Sprint(i)}, StatusCode: 200, OK: true}, 3); err != nil {
			t.Fatal(err)
		}
	}
	runs, err := repo.ListRuns(ctx, c.ID, 10)
	if err != nil || len(runs) != 3 || runs[0].Params["i"] != "4" || runs[0].UserID != userID {
		t.Fatalf("runs: %+v, %v", runs, err)
	}

	if err := repo.Delete(ctx, c.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Get(ctx, c.ID); !errors.Is(err, connectors.ErrNotFound) {
		t.Fatalf("get after delete: %v", err)
	}
	if err := repo.Delete(ctx, c.ID); !errors.Is(err, connectors.ErrNotFound) {
		t.Fatalf("second delete: %v", err)
	}
}
