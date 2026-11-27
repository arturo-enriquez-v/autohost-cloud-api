// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package handler

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/arturo/autohost-cloud-api/internal/filetransfer"
)

// Internal errors carry OS details (absolute host paths): they must not reach the client.
func TestWriteFileOpErrorHidesInternalDetails(t *testing.T) {
	rec := httptest.NewRecorder()
	writeFileOpError(rec, errors.New("rename /home/someone/.autohost/shared/a /home/someone/.autohost/shared/b: input/output error"))
	if rec.Code != http.StatusInternalServerError || strings.Contains(rec.Body.String(), "/home/") {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	writeFileOpError(rec, &filetransfer.OpError{Kind: filetransfer.KindInvalid, Msg: "cannot move a folder inside itself"})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "inside itself") {
		t.Fatalf("expected errors keep their message: %d %s", rec.Code, rec.Body.String())
	}
}
