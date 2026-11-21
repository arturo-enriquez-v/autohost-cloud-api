// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

// Package api embeds the OpenAPI specification of the REST API.
package api

import _ "embed"

// OpenAPISpec is the OpenAPI 3.1 document (YAML) describing the REST API.
//
//go:embed openapi.yaml
var OpenAPISpec []byte
