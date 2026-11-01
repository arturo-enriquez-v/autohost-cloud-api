// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package platform

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"log"
	"math/big"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// PKIManager manages the internal Certificate Authority (CA), gRPC server
// certificate, and signs client certificates for enrolled node agents (mTLS).
type PKIManager struct {
	mu sync.RWMutex

	certsDir string

	caCert    *x509.Certificate
	caKey     crypto.Signer
	caCertPEM []byte
	caPool    *x509.CertPool

	serverTLSCert tls.Certificate
	serverX509    *x509.Certificate
	serverKey     crypto.Signer
}

// NewPKIManager initializes or loads the CA and server certificates from disk.
// By default it stores certificates in GRPC_CERTS_DIR (or "./certs").
func NewPKIManager() (*PKIManager, error) {
	return InitPKI(PKICertsDir())
}

// InitPKI initializes a PKIManager using the specified directory for storing
// auto-generated certificates when explicit paths are not configured via env vars.
func InitPKI(certsDir string) (*PKIManager, error) {
	if err := os.MkdirAll(certsDir, 0750); err != nil {
		return nil, fmt.Errorf("pki: create certs dir %s: %w", certsDir, err)
	}

	caCertPath := envOrDefault("GRPC_CA_CERT_PATH", filepath.Join(certsDir, "ca.crt"))
	caKeyPath := envOrDefault("GRPC_CA_KEY_PATH", filepath.Join(certsDir, "ca.key"))
	serverCertPath := envOrDefault("GRPC_SERVER_CERT_PATH", filepath.Join(certsDir, "server.crt"))
	serverKeyPath := envOrDefault("GRPC_SERVER_KEY_PATH", filepath.Join(certsDir, "server.key"))

	m := &PKIManager{
		certsDir: certsDir,
	}

	if err := m.loadOrCreateCA(caCertPath, caKeyPath); err != nil {
		return nil, err
	}

	dnsNames, ipAddrs := collectDefaultServerSANs()
	if err := m.loadOrCreateServerCert(serverCertPath, serverKeyPath, dnsNames, ipAddrs); err != nil {
		return nil, err
	}

	return m, nil
}

// CACertPEM returns the PEM-encoded Root CA certificate so agents can trust the API.
func (m *PKIManager) CACertPEM() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return string(m.caCertPEM)
}

// SignClientCSR validates a PEM-encoded PKCS#10 Certificate Signing Request from a node
// and signs a client certificate bound to the given nodeID (CommonName = nodeID).
func (m *PKIManager) SignClientCSR(csrPEM []byte, nodeID string) ([]byte, error) {
	nodeID = strings.TrimSpace(nodeID)
	if nodeID == "" {
		return nil, errors.New("pki: nodeID is required to sign client certificate")
	}

	block, _ := pem.Decode(csrPEM)
	if block == nil || (block.Type != "CERTIFICATE REQUEST" && block.Type != "NEW CERTIFICATE REQUEST") {
		return nil, errors.New("pki: invalid CSR PEM block")
	}

	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("pki: parse CSR: %w", err)
	}
	if err := csr.CheckSignature(); err != nil {
		return nil, fmt.Errorf("pki: invalid CSR signature: %w", err)
	}

	m.mu.RLock()
	caCert := m.caCert
	caKey := m.caKey
	m.mu.RUnlock()

	if caCert == nil || caKey == nil {
		return nil, errors.New("pki: CA private key is not available to sign CSR")
	}

	serial, err := randomSerial()
	if err != nil {
		return nil, err
	}

	now := time.Now()
	spiffeURI, _ := url.Parse("spiffe://autohost.cloud/node/" + nodeID)

	template := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   nodeID,
			Organization: []string{"Autohost Node"},
		},
		DNSNames:              []string{nodeID},
		URIs:                  []*url.URL{spiffeURI},
		NotBefore:             now.Add(-5 * time.Minute),
		NotAfter:              now.Add(365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, template, caCert, csr.PublicKey, caKey)
	if err != nil {
		return nil, fmt.Errorf("pki: create client certificate: %w", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE",
		Bytes: derBytes,
	})
	return certPEM, nil
}

// IssueClientCert generates a fresh ECDSA P-256 keypair and signs a client certificate
// for the given nodeID. Used when the enrollment request does not supply a CSR.
func (m *PKIManager) IssueClientCert(nodeID string) (certPEM []byte, keyPEM []byte, err error) {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, fmt.Errorf("pki: generate client key: %w", err)
	}

	csrTemplate := &x509.CertificateRequest{
		Subject: pkix.Name{
			CommonName:   nodeID,
			Organization: []string{"Autohost Node"},
		},
	}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, csrTemplate, priv)
	if err != nil {
		return nil, nil, fmt.Errorf("pki: create internal CSR: %w", err)
	}
	csrPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "CERTIFICATE REQUEST",
		Bytes: csrDER,
	})

	certPEM, err = m.SignClientCSR(csrPEM, nodeID)
	if err != nil {
		return nil, nil, err
	}

	keyDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		return nil, nil, fmt.Errorf("pki: marshal client private key: %w", err)
	}
	keyPEM = pem.EncodeToMemory(&pem.Block{
		Type:  "EC PRIVATE KEY",
		Bytes: keyDER,
	})

	return certPEM, keyPEM, nil
}

// EnsureServerSAN guarantees that the server certificate covers the given host or IP.
// If the host/IP is not yet present in the server certificate's SANs and the CA key is
// available, it re-issues the server certificate with the additional SAN.
func (m *PKIManager) EnsureServerSAN(hostOrAddr string) {
	host := extractHost(hostOrAddr)
	if host == "" {
		return
	}

	m.mu.RLock()
	srvCert := m.serverX509
	hasCAKey := m.caKey != nil
	m.mu.RUnlock()

	if srvCert == nil || !hasCAKey {
		return
	}

	if certCoversHost(srvCert, host) {
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Double-check under write lock
	if certCoversHost(m.serverX509, host) {
		return
	}

	dnsNames := append([]string{}, m.serverX509.DNSNames...)
	ipAddrs := append([]net.IP{}, m.serverX509.IPAddresses...)

	if ip := net.ParseIP(host); ip != nil {
		ipAddrs = appendUniqueIP(ipAddrs, ip)
	} else {
		dnsNames = appendUniqueString(dnsNames, host)
	}

	serverCertPath := envOrDefault("GRPC_SERVER_CERT_PATH", filepath.Join(m.certsDir, "server.crt"))
	serverKeyPath := envOrDefault("GRPC_SERVER_KEY_PATH", filepath.Join(m.certsDir, "server.key"))

	if err := m.generateServerCertLocked(serverCertPath, serverKeyPath, dnsNames, ipAddrs); err != nil {
		log.Printf("[PKI] warn: failed to add SAN %q to server cert: %v", host, err)
	} else {
		log.Printf("[PKI] dynamically added SAN %q to gRPC server certificate", host)
	}
}

// ServerTLSConfig returns the *tls.Config for the gRPC server.
func (m *PKIManager) ServerTLSConfig() (*tls.Config, error) {
	m.mu.RLock()
	caPool := m.caPool
	m.mu.RUnlock()

	if caPool == nil {
		return nil, errors.New("pki: CA pool not initialized")
	}

	clientAuth := ResolveClientAuthMode()

	return &tls.Config{
		MinVersion: tls.VersionTLS12,
		ClientCAs:  caPool,
		ClientAuth: clientAuth,
		GetCertificate: func(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
			if hello != nil {
				if hello.ServerName != "" {
					m.EnsureServerSAN(hello.ServerName)
				}
				if hello.Conn != nil && hello.Conn.LocalAddr() != nil {
					m.EnsureServerSAN(hello.Conn.LocalAddr().String())
				}
			}
			m.mu.RLock()
			cert := m.serverTLSCert
			m.mu.RUnlock()
			return &cert, nil
		},
	}, nil
}

// ResolveClientAuthMode reads GRPC_MTLS_MODE and returns the appropriate tls.ClientAuthType.
// Defaults to tls.RequireAndVerifyClientCert (strict mTLS).
func ResolveClientAuthMode() tls.ClientAuthType {
	mode := strings.ToLower(strings.TrimSpace(os.Getenv("GRPC_MTLS_MODE")))
	switch mode {
	case "optional", "verify_if_given", "transitional":
		return tls.VerifyClientCertIfGiven
	case "none":
		return tls.NoClientCert
	default:
		return tls.RequireAndVerifyClientCert
	}
}

// IsGRPCTLSEnabled returns true unless GRPC_TLS_ENABLED is explicitly set to "false" or "0".
func IsGRPCTLSEnabled() bool {
	val := strings.ToLower(strings.TrimSpace(os.Getenv("GRPC_TLS_ENABLED")))
	return val != "false" && val != "0" && val != "no"
}

func (m *PKIManager) loadOrCreateCA(certPath, keyPath string) error {
	if fileExists(certPath) && fileExists(keyPath) {
		certPEM, err := os.ReadFile(certPath)
		if err != nil {
			return fmt.Errorf("pki: read CA cert: %w", err)
		}
		keyPEM, err := os.ReadFile(keyPath)
		if err != nil {
			return fmt.Errorf("pki: read CA key: %w", err)
		}

		tlsPair, err := tls.X509KeyPair(certPEM, keyPEM)
		if err != nil {
			return fmt.Errorf("pki: parse CA keypair: %w", err)
		}
		x509Cert, err := x509.ParseCertificate(tlsPair.Certificate[0])
		if err != nil {
			return fmt.Errorf("pki: parse CA x509: %w", err)
		}
		signer, ok := tlsPair.PrivateKey.(crypto.Signer)
		if !ok {
			return errors.New("pki: CA private key does not implement crypto.Signer")
		}

		pool := x509.NewCertPool()
		pool.AddCert(x509Cert)

		m.caCert = x509Cert
		m.caKey = signer
		m.caCertPEM = certPEM
		m.caPool = pool
		log.Printf("[PKI] loaded Root CA from %s (subject: %s)", certPath, x509Cert.Subject.CommonName)
		return nil
	}

	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("pki: generate CA key: %w", err)
	}

	serial, err := randomSerial()
	if err != nil {
		return err
	}

	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   "Autohost Internal Root CA",
			Organization: []string{"Autohost Cloud"},
		},
		NotBefore:             now.Add(-10 * time.Minute),
		NotAfter:              now.Add(10 * 365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            1,
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, template, template, &priv.PublicKey, priv)
	if err != nil {
		return fmt.Errorf("pki: create CA certificate: %w", err)
	}

	x509Cert, err := x509.ParseCertificate(derBytes)
	if err != nil {
		return fmt.Errorf("pki: parse generated CA certificate: %w", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: derBytes})
	keyDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		return fmt.Errorf("pki: marshal CA key: %w", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	if err := os.MkdirAll(filepath.Dir(certPath), 0750); err != nil {
		return err
	}
	if err := os.WriteFile(certPath, certPEM, 0644); err != nil {
		return fmt.Errorf("pki: write CA cert %s: %w", certPath, err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0600); err != nil {
		return fmt.Errorf("pki: write CA key %s: %w", keyPath, err)
	}

	pool := x509.NewCertPool()
	pool.AddCert(x509Cert)

	m.caCert = x509Cert
	m.caKey = priv
	m.caCertPEM = certPEM
	m.caPool = pool
	log.Printf("[PKI] generated new Root CA at %s", certPath)
	return nil
}

func (m *PKIManager) loadOrCreateServerCert(certPath, keyPath string, dnsNames []string, ipAddrs []net.IP) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if fileExists(certPath) && fileExists(keyPath) {
		tlsPair, err := tls.LoadX509KeyPair(certPath, keyPath)
		if err == nil && len(tlsPair.Certificate) > 0 {
			x509Cert, err := x509.ParseCertificate(tlsPair.Certificate[0])
			if err == nil && time.Now().Add(7*24*time.Hour).Before(x509Cert.NotAfter) {
				// Verify signed by current CA
				if err := x509Cert.CheckSignatureFrom(m.caCert); err == nil {
					signer, _ := tlsPair.PrivateKey.(crypto.Signer)
					m.serverTLSCert = tlsPair
					m.serverX509 = x509Cert
					m.serverKey = signer

					// Ensure all default SANs are covered
					missingSAN := false
					for _, d := range dnsNames {
						if !certCoversHost(x509Cert, d) {
							missingSAN = true
							break
						}
					}
					for _, ip := range ipAddrs {
						if !certCoversHost(x509Cert, ip.String()) {
							missingSAN = true
							break
						}
					}
					if !missingSAN {
						log.Printf("[PKI] loaded gRPC server certificate from %s (DNS: %v, IPs: %v)", certPath, x509Cert.DNSNames, x509Cert.IPAddresses)
						return nil
					}
					// Merge existing SANs with new SANs and regenerate
					for _, d := range x509Cert.DNSNames {
						dnsNames = appendUniqueString(dnsNames, d)
					}
					for _, ip := range x509Cert.IPAddresses {
						ipAddrs = appendUniqueIP(ipAddrs, ip)
					}
				}
			}
		}
	}

	return m.generateServerCertLocked(certPath, keyPath, dnsNames, ipAddrs)
}

func (m *PKIManager) generateServerCertLocked(certPath, keyPath string, dnsNames []string, ipAddrs []net.IP) error {
	var priv crypto.Signer
	if m.serverKey != nil {
		priv = m.serverKey
	} else {
		ecKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return fmt.Errorf("pki: generate server key: %w", err)
		}
		priv = ecKey
	}

	serial, err := randomSerial()
	if err != nil {
		return err
	}

	now := time.Now()
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   "autohost-cloud-api",
			Organization: []string{"Autohost Cloud"},
		},
		DNSNames:              dnsNames,
		IPAddresses:           ipAddrs,
		NotBefore:             now.Add(-10 * time.Minute),
		NotAfter:              now.Add(365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, template, m.caCert, priv.Public(), m.caKey)
	if err != nil {
		return fmt.Errorf("pki: create server certificate: %w", err)
	}

	x509Cert, err := x509.ParseCertificate(derBytes)
	if err != nil {
		return fmt.Errorf("pki: parse server certificate: %w", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: derBytes})

	var keyPEM []byte
	if ecKey, ok := priv.(*ecdsa.PrivateKey); ok {
		keyDER, err := x509.MarshalECPrivateKey(ecKey)
		if err != nil {
			return fmt.Errorf("pki: marshal server key: %w", err)
		}
		keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	} else {
		pkcs8, err := x509.MarshalPKCS8PrivateKey(priv)
		if err != nil {
			return fmt.Errorf("pki: marshal server pkcs8 key: %w", err)
		}
		keyPEM = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8})
	}

	if err := os.MkdirAll(filepath.Dir(certPath), 0750); err != nil {
		return err
	}
	if err := os.WriteFile(certPath, certPEM, 0644); err != nil {
		return fmt.Errorf("pki: write server cert: %w", err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0600); err != nil {
		return fmt.Errorf("pki: write server key: %w", err)
	}

	tlsPair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return fmt.Errorf("pki: load generated server keypair: %w", err)
	}

	m.serverTLSCert = tlsPair
	m.serverX509 = x509Cert
	m.serverKey = priv
	log.Printf("[PKI] issued gRPC server certificate at %s (DNS: %v, IPs: %v)", certPath, dnsNames, ipAddrs)
	return nil
}

func collectDefaultServerSANs() ([]string, []net.IP) {
	dnsNames := []string{"localhost", "autohost-cloud-api", "autohost-api"}
	ipAddrs := []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")}

	if hn, err := os.Hostname(); err == nil && hn != "" {
		dnsNames = appendUniqueString(dnsNames, hn)
	}

	// Local network interface IPs
	if addrs, err := net.InterfaceAddrs(); err == nil {
		for _, a := range addrs {
			var ip net.IP
			switch v := a.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip != nil && !ip.IsUnspecified() {
				ipAddrs = appendUniqueIP(ipAddrs, ip)
			}
		}
	}

	// Outbound UDP interface IP
	if conn, err := net.Dial("udp", "8.8.8.8:80"); err == nil {
		if udpAddr, ok := conn.LocalAddr().(*net.UDPAddr); ok && udpAddr.IP != nil {
			ipAddrs = appendUniqueIP(ipAddrs, udpAddr.IP)
		}
		conn.Close()
	}

	// Env vars: GRPC_ADDRESS, API_PUBLIC_URL, API_URL, GRPC_TLS_SANS
	for _, envKey := range []string{"GRPC_ADDRESS", "API_PUBLIC_URL", "API_URL"} {
		if raw := strings.TrimSpace(os.Getenv(envKey)); raw != "" {
			if h := extractHost(raw); h != "" {
				if ip := net.ParseIP(h); ip != nil {
					ipAddrs = appendUniqueIP(ipAddrs, ip)
				} else {
					dnsNames = appendUniqueString(dnsNames, h)
				}
			}
		}
	}

	if extra := strings.TrimSpace(os.Getenv("GRPC_TLS_SANS")); extra != "" {
		for _, part := range strings.Split(extra, ",") {
			if h := extractHost(part); h != "" {
				if ip := net.ParseIP(h); ip != nil {
					ipAddrs = appendUniqueIP(ipAddrs, ip)
				} else {
					dnsNames = appendUniqueString(dnsNames, h)
				}
			}
		}
	}

	return dnsNames, ipAddrs
}

func extractHost(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	if strings.Contains(s, "://") {
		if u, err := url.Parse(s); err == nil {
			return u.Hostname()
		}
	}
	s = strings.TrimRight(s, "/")
	if strings.HasPrefix(s, ":") {
		return ""
	}
	if h, _, err := net.SplitHostPort(s); err == nil {
		return h
	}
	return s
}

func certCoversHost(cert *x509.Certificate, host string) bool {
	if cert == nil || host == "" {
		return true
	}
	return cert.VerifyHostname(host) == nil
}

func appendUniqueString(slice []string, val string) []string {
	val = strings.TrimSpace(val)
	if val == "" {
		return slice
	}
	for _, existing := range slice {
		if strings.EqualFold(existing, val) {
			return slice
		}
	}
	return append(slice, val)
}

func appendUniqueIP(slice []net.IP, ip net.IP) []net.IP {
	if ip == nil {
		return slice
	}
	for _, existing := range slice {
		if existing.Equal(ip) {
			return slice
		}
	}
	return append(slice, ip)
}

func randomSerial() (*big.Int, error) {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	s, err := rand.Int(rand.Reader, limit)
	if err != nil {
		return nil, fmt.Errorf("pki: generate serial: %w", err)
	}
	return s, nil
}

func envOrDefault(key, def string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" {
		return v
	}
	return def
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
