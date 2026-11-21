// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/arturo/autohost-cloud-api/internal/core/infra"
	"github.com/arturo/autohost-cloud-api/internal/core/node"
	"github.com/arturo/autohost-cloud-api/internal/handler/middleware"
	"github.com/arturo/autohost-cloud-api/internal/platform"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

type EnrollmentHandler struct {
	service          *node.EnrollmentService
	nodeService      *node.NodeService
	nodeTokenService *node.NodeTokenService
	vmRepo           infra.VMRepository
	pki              *platform.PKIManager
}

func NewEnrollmentHandler(service *node.EnrollmentService, nodeService *node.NodeService, nodeTokenService *node.NodeTokenService, vmRepo infra.VMRepository, pki *platform.PKIManager) *EnrollmentHandler {
	return &EnrollmentHandler{
		service:          service,
		nodeService:      nodeService,
		nodeTokenService: nodeTokenService,
		vmRepo:           vmRepo,
		pki:              pki,
	}
}

func (h *EnrollmentHandler) Routes(nodeAuthMiddleware func(http.Handler) http.Handler) chi.Router {
	r := chi.NewRouter()

	r.Group(func(protected chi.Router) {
		protected.Use(middleware.Auth)
		protected.Post("/generate", h.CreateEnrollToken)
	})

	// Agent endpoint: renew the node's mTLS client certificate (authenticated with the node token).
	r.Group(func(nodeRouter chi.Router) {
		nodeRouter.Use(nodeAuthMiddleware)
		nodeRouter.Post("/renew-cert", h.RenewClientCert)
	})

	r.With(middleware.RateLimitByIP(enrollIPLimiter)).Post("/enroll", h.EnrollNode)

	return r
}

type enrollTokenResponse struct {
	Token       string    `json:"token"`
	ExpiresAt   time.Time `json:"expires_at"`
	ApiURL      string    `json:"api_url,omitempty"`
	TailscaleIP string    `json:"tailscale_ip,omitempty"`
}

func (h *EnrollmentHandler) CreateEnrollToken(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil || claims.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	// Generar token de enrrolamiento
	plainToken, hash, err := platform.GenerateEnrollToken()
	if err != nil {
		log.Printf("[ERROR] generate enroll token: %v", err)
		http.Error(w, "could not generate token", http.StatusInternalServerError)
		return
	}

	// Establecer expiración (1 hora)
	expiresAt := time.Now().Add(1 * time.Hour)

	// Guardar en BD (guardamos el hash, no el token plano)
	if err := h.service.CreateEnrollToken(hash, claims.UserID, expiresAt); err != nil {
		if err == node.ErrInvalidEnrollTokenData {
			http.Error(w, "invalid token data", http.StatusBadRequest)
			return
		}
		log.Printf("[ERROR] create enroll token: %v", err)
		http.Error(w, "could not create token", http.StatusInternalServerError)
		return
	}

	log.Printf("[INFO] enroll token created for user: %s", claims.UserID)

	// Determinar URL sugerida para conectar nodos remotos
	var apiURL string
	var tailscaleIP string
	if h.nodeService != nil {
		if localNode, err := h.nodeService.FindLocalNode(); err == nil && localNode != nil {
			if localNode.IPVPN != nil && *localNode.IPVPN != "" {
				tailscaleIP = *localNode.IPVPN
				apiURL = fmt.Sprintf("http://%s:8080", tailscaleIP)
			} else if localNode.IPLocal != "" {
				apiURL = fmt.Sprintf("http://%s:8080", localNode.IPLocal)
			}
		}
	}
	if apiURL == "" {
		if extURL := os.Getenv("NEXT_PUBLIC_API_URL"); extURL != "" {
			apiURL = extURL
		} else if r != nil && r.Host != "" {
			apiURL = fmt.Sprintf("http://%s", r.Host)
		}
	}

	// Devolver token plano al usuario (solo esta vez) junto con la URL/IP sugerida
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(enrollTokenResponse{
		Token:       plainToken,
		ExpiresAt:   expiresAt,
		ApiURL:      apiURL,
		TailscaleIP: tailscaleIP,
	})
}

func (h *EnrollmentHandler) EnrollNode(w http.ResponseWriter, r *http.Request) {
	var req struct {
		EnrollToken  string `json:"enroll_token"`
		Hostname     string `json:"hostname"`
		IPLocal      string `json:"ip_local"`
		OS           string `json:"os"`
		Arch         string `json:"arch"`
		VersionAgent string `json:"version_agent"`
		CSRPEM       string `json:"csr_pem"`
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}

	if req.EnrollToken == "" {
		http.Error(w, "missing enroll_token", http.StatusBadRequest)
		return
	}

	hash := platform.HashEnrollToken(req.EnrollToken)

	enroll, err := h.service.FindEnrollTokenByHash(hash)
	if err != nil {
		http.Error(w, "invalid token", http.StatusUnauthorized)
		return
	}

	if enroll.ConsumedAt != nil {
		http.Error(w, "token already used", http.StatusUnauthorized)
		return
	}

	if time.Now().After(enroll.ExpiresAt) {
		http.Error(w, "token expired", http.StatusUnauthorized)
		return
	}

	// Consume the token before creating anything: the conditional UPDATE makes this the single point
	// that decides which of several concurrent requests with the same token wins.
	if err := h.service.MarkTokenAsUsed(enroll.Token, time.Now()); err != nil {
		if errors.Is(err, node.ErrEnrollTokenAlreadyUsed) {
			http.Error(w, "token already used", http.StatusUnauthorized)
			return
		}
		log.Printf("[ERROR] enroll: consume token: %v", err)
		http.Error(w, "could not validate token", http.StatusInternalServerError)
		return
	}

	ipLocal := strings.TrimSpace(req.IPLocal)
	if net.ParseIP(ipLocal) == nil {
		remoteHost, _, err := net.SplitHostPort(r.RemoteAddr)
		if err == nil && net.ParseIP(remoteHost) != nil && remoteHost != "127.0.0.1" && remoteHost != "::1" {
			ipLocal = remoteHost
		} else {
			ipLocal = ""
		}
	}

	hostname := strings.TrimSpace(req.Hostname)
	if hostname == "" {
		hostname = "node-" + uuid.NewString()[:8]
	}

	node := &node.Node{
		Hostname:     hostname,
		IPLocal:      ipLocal,
		OS:           strings.TrimSpace(req.OS),
		Arch:         strings.TrimSpace(req.Arch),
		VersionAgent: strings.TrimSpace(req.VersionAgent),
		OwnerID:      &enroll.UserID,
	}

	createdNode, err := h.nodeService.Register(node)
	if err != nil {
		log.Printf("[ERROR] enroll: failed to register node: %v", err)
		http.Error(w, "failed to create node", http.StatusInternalServerError)
		return
	}

	plainToken, hashToken, err := platform.GenerateTokenApi()
	if err != nil {
		log.Printf("[ERROR] enroll: failed to generate api token: %v", err)
		http.Error(w, "failed to generate api token", http.StatusInternalServerError)
		return
	}

	err = h.nodeTokenService.CreateNodeToken(createdNode.ID, hashToken)
	if err != nil {
		log.Printf("[ERROR] enroll: failed to save api token: %v", err)
		http.Error(w, "failed to save api token", http.StatusInternalServerError)
		return
	}

	if h.vmRepo != nil {
		_ = h.vmRepo.LinkNodeByHostname(r.Context(), enroll.UserID, req.Hostname, createdNode.ID, req.IPLocal)
	}

	grpcAddr := grpcAddressForRequest(r)

	respPayload := map[string]string{
		"node_id":      createdNode.ID,
		"api_token":    plainToken,
		"grpc_address": grpcAddr,
	}

	if h.pki != nil && platform.IsGRPCTLSEnabled() {
		h.pki.EnsureServerSAN(r.Host)
		h.pki.EnsureServerSAN(grpcAddr)

		if strings.TrimSpace(req.CSRPEM) != "" {
			clientCert, err := h.pki.SignClientCSR([]byte(req.CSRPEM), createdNode.ID)
			if err != nil {
				log.Printf("[ERROR] sign client CSR for node %s: %v", createdNode.ID, err)
				http.Error(w, "failed to sign client certificate", http.StatusBadRequest)
				return
			}
			respPayload["ca_cert"] = h.pki.CACertPEM()
			respPayload["client_cert"] = string(clientCert)
		} else {
			clientCert, clientKey, err := h.pki.IssueClientCert(createdNode.ID)
			if err != nil {
				log.Printf("[ERROR] issue client cert for node %s: %v", createdNode.ID, err)
				http.Error(w, "failed to issue client certificate", http.StatusInternalServerError)
				return
			}
			respPayload["ca_cert"] = h.pki.CACertPEM()
			respPayload["client_cert"] = string(clientCert)
			respPayload["client_key"] = string(clientKey)
		}
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(respPayload)
}

// grpcAddressForRequest returns the gRPC address that agents should connect to.
// If GRPC_ADDRESS is not set, it derives the host from the incoming HTTP request's Host header
// so remote nodes get a routable host:port instead of a bare ":9090".
func grpcAddressForRequest(r *http.Request) string {
	addr := grpcAddress()
	if strings.HasPrefix(addr, ":") && r != nil && r.Host != "" {
		host := r.Host
		if h, _, err := net.SplitHostPort(r.Host); err == nil {
			host = h
		}
		if host != "" {
			return net.JoinHostPort(host, strings.TrimPrefix(addr, ":"))
		}
	}
	return addr
}

// grpcAddress returns the gRPC address that agents should connect to.
// It reads GRPC_ADDRESS from the environment; if not set it falls back to
// ":<GRPC_PORT>" so local dev still works without extra configuration.
func grpcAddress() string {
	if addr := os.Getenv("GRPC_ADDRESS"); addr != "" {
		return cleanGRPCAddress(addr)
	}
	port := os.Getenv("GRPC_PORT")
	if port == "" {
		port = "9090"
	}
	return ":" + port
}

func cleanGRPCAddress(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	hasHTTPS := strings.HasPrefix(raw, "https://")
	hasHTTP := strings.HasPrefix(raw, "http://")
	clean := strings.TrimPrefix(raw, "https://")
	clean = strings.TrimPrefix(clean, "http://")
	clean = strings.TrimRight(clean, "/")

	if strings.HasPrefix(clean, ":") {
		return clean
	}

	if _, _, err := net.SplitHostPort(clean); err == nil {
		return clean
	}

	if hasHTTPS {
		return clean + ":443"
	}
	if hasHTTP {
		return clean + ":9090"
	}
	if strings.Contains(clean, ".") {
		return clean + ":443"
	}
	return clean + ":9090"
}

// renewCertLimiter caps certificate renewals per node (legitimate agents renew every few weeks).
var renewCertLimiter = middleware.NewRateLimiter(10, time.Hour)

// RenewClientCert signs a new mTLS client certificate for the authenticated node.
// POST /v1/enrollments/renew-cert  {"csr_pem": "..."}
//
// The certificate identity (CN) is always the node ID bound to the caller's node token; the CSR subject is
// ignored. Revoked or deleted nodes are rejected by the node auth middleware before reaching this handler.
func (h *EnrollmentHandler) RenewClientCert(w http.ResponseWriter, r *http.Request) {
	nodeToken := middleware.GetNodeToken(r.Context())
	if nodeToken == nil || nodeToken.NodeID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if h.pki == nil || !platform.IsGRPCTLSEnabled() {
		http.Error(w, "mTLS is not enabled on this server", http.StatusConflict)
		return
	}
	if ok, retry := renewCertLimiter.Allow("node:" + nodeToken.NodeID); !ok {
		middleware.WriteRateLimited(w, retry)
		return
	}

	var req struct {
		CSRPEM string `json:"csr_pem"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || strings.TrimSpace(req.CSRPEM) == "" {
		http.Error(w, "csr_pem is required", http.StatusBadRequest)
		return
	}

	ttl := platform.RenewedClientCertTTL()
	certPEM, err := h.pki.SignClientCSRWithTTL([]byte(req.CSRPEM), nodeToken.NodeID, ttl)
	if err != nil {
		log.Printf("[WARN] renew client cert for node %s: %v", nodeToken.NodeID, err)
		http.Error(w, "invalid certificate signing request", http.StatusBadRequest)
		return
	}
	log.Printf("[PKI] renewed client certificate for node %s (valid %s)", nodeToken.NodeID, ttl)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"client_cert": string(certPEM),
		"ca_cert":     h.pki.CACertPEM(),
		"expires_at":  time.Now().Add(ttl).UTC().Format(time.RFC3339),
	})
}
