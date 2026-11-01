// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package platform

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"testing"
)

func TestPKIManager_InitAndSignClientCSR(t *testing.T) {
	dir := t.TempDir()

	pki, err := InitPKI(dir)
	if err != nil {
		t.Fatalf("InitPKI failed: %v", err)
	}

	if pki.CACertPEM() == "" {
		t.Fatal("expected non-empty CA cert PEM")
	}

	// Verify reloading from disk reuses the same CA
	pkiReloaded, err := InitPKI(dir)
	if err != nil {
		t.Fatalf("InitPKI reload failed: %v", err)
	}
	if pkiReloaded.CACertPEM() != pki.CACertPEM() {
		t.Fatal("expected reloaded CA cert PEM to match original")
	}

	// Generate client CSR
	clientPriv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate client key: %v", err)
	}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: "requested-cn"},
	}, clientPriv)
	if err != nil {
		t.Fatalf("create CSR: %v", err)
	}
	csrPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER})

	nodeID := "node-uuid-1234"
	certPEM, err := pki.SignClientCSR(csrPEM, nodeID)
	if err != nil {
		t.Fatalf("SignClientCSR failed: %v", err)
	}

	block, _ := pem.Decode(certPEM)
	if block == nil {
		t.Fatal("expected valid PEM block for signed client cert")
	}
	clientCert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parse client cert: %v", err)
	}

	if clientCert.Subject.CommonName != nodeID {
		t.Errorf("expected CN %q, got %q", nodeID, clientCert.Subject.CommonName)
	}

	// Verify client cert against CA pool
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM([]byte(pki.CACertPEM())) {
		t.Fatal("failed to append CA PEM to pool")
	}
	if _, err := clientCert.Verify(x509.VerifyOptions{
		Roots:     roots,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}); err != nil {
		t.Fatalf("client cert verification against CA failed: %v", err)
	}

	// Verify ServerTLSConfig and EnsureServerSAN
	pki.EnsureServerSAN("192.168.101.2:9090")
	tlsCfg, err := pki.ServerTLSConfig()
	if err != nil {
		t.Fatalf("ServerTLSConfig failed: %v", err)
	}
	if tlsCfg.ClientAuth != tls.RequireAndVerifyClientCert {
		t.Errorf("expected RequireAndVerifyClientCert, got %v", tlsCfg.ClientAuth)
	}

	srvCert, err := tlsCfg.GetCertificate(&tls.ClientHelloInfo{ServerName: "192.168.101.2"})
	if err != nil || srvCert == nil {
		t.Fatalf("GetCertificate failed: %v", err)
	}
	parsedSrv, err := x509.ParseCertificate(srvCert.Certificate[0])
	if err != nil {
		t.Fatalf("parse server cert: %v", err)
	}
	if err := parsedSrv.VerifyHostname("192.168.101.2"); err != nil {
		t.Errorf("expected server cert to cover 192.168.101.2: %v", err)
	}
}
