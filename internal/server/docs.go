// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package server

import (
	"net/http"
	"os"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/arturo/autohost-cloud-api/api"
)

// apiDocsEnabled reports whether the API reference is served. Off by default; set API_DOCS_ENABLED=true.
func apiDocsEnabled() bool {
	v := strings.ToLower(strings.TrimSpace(os.Getenv("API_DOCS_ENABLED")))
	return v == "true" || v == "1"
}

// docsPage renders the spec with Scalar (https://github.com/scalar/scalar), loaded from jsDelivr.
const docsPage = `<!doctype html>
<html>
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>AutoHost Cloud API</title>
</head>
<body>
  <script id="api-reference" data-url="/openapi.yaml"></script>
  <script src="https://cdn.jsdelivr.net/npm/@scalar/api-reference@1.75.0"></script>
</body>
</html>`

// mountDocs serves the raw spec at /openapi.yaml and an interactive reference at /docs.
func mountDocs(r chi.Router) {
	r.Get("/openapi.yaml", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/yaml")
		_, _ = w.Write(api.OpenAPISpec)
	})
	r.Get("/docs", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(docsPage))
	})
}
