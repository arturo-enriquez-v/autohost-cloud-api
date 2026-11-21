// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package handler_test

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/arturo/autohost-cloud-api/internal/core/node"
	"github.com/arturo/autohost-cloud-api/internal/handler"
	"github.com/arturo/autohost-cloud-api/internal/handler/middleware"
	"github.com/arturo/autohost-cloud-api/internal/platform"
)

func makeCSR(t *testing.T, cn string) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: cn}}, key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der}))
}

func renewRequest(t *testing.T, h *handler.EnrollmentHandler, nodeID, csr string) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"csr_pem": csr})
	req := httptest.NewRequest(http.MethodPost, "/renew-cert", bytes.NewReader(body))
	if nodeID != "" {
		req = req.WithContext(middleware.ContextWithNodeToken(req.Context(), &node.NodeToken{NodeID: nodeID}))
	}
	rec := httptest.NewRecorder()
	h.RenewClientCert(rec, req)
	return rec
}

func TestRenewClientCert(t *testing.T) {
	t.Setenv("GRPC_TLS_ENABLED", "true")
	t.Setenv("GRPC_RENEWED_CERT_TTL", "720h")
	pki, err := platform.InitPKI(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	h := handler.NewEnrollmentHandler(nil, nil, nil, nil, pki)

	// The CSR asks for another node's identity: the certificate must still be bound to the caller.
	rec := renewRequest(t, h, "node-caller", makeCSR(t, "node-victim"))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	block, _ := pem.Decode([]byte(resp["client_cert"]))
	if block == nil {
		t.Fatal("no certificate in response")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if cert.Subject.CommonName != "node-caller" {
		t.Fatalf("certificate CN = %q, want the authenticated node", cert.Subject.CommonName)
	}
	if life := cert.NotAfter.Sub(time.Now()); life < 719*time.Hour || life > 721*time.Hour {
		t.Fatalf("unexpected certificate lifetime %s (want GRPC_RENEWED_CERT_TTL=720h)", life)
	}
	if resp["ca_cert"] == "" {
		t.Fatal("expected CA certificate in response")
	}

	if rec := renewRequest(t, h, "", makeCSR(t, "x")); rec.Code != http.StatusUnauthorized {
		t.Errorf("without node token: got %d, want 401", rec.Code)
	}
	if rec := renewRequest(t, h, "node-caller", "not a csr"); rec.Code != http.StatusBadRequest {
		t.Errorf("invalid CSR: got %d, want 400", rec.Code)
	}
}
