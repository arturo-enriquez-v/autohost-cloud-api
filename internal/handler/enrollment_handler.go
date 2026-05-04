package handler

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/arturo/autohost-cloud-api/internal/domain/enrollment"
	"github.com/arturo/autohost-cloud-api/internal/domain/node"
	nodetoken "github.com/arturo/autohost-cloud-api/internal/domain/node_token"
	"github.com/arturo/autohost-cloud-api/internal/handler/middleware"
	"github.com/arturo/autohost-cloud-api/internal/platform"
	"github.com/go-chi/chi/v5"
)

type EnrollmentHandler struct {
	service          *enrollment.Service
	nodeService      *node.Service
	nodeTokenService *nodetoken.Service
}

func NewEnrollmentHandler(service *enrollment.Service, nodeService *node.Service, nodeTokenService *nodetoken.Service) *EnrollmentHandler {
	return &EnrollmentHandler{service: service, nodeService: nodeService, nodeTokenService: nodeTokenService}
}

func (h *EnrollmentHandler) Routes() chi.Router {
	r := chi.NewRouter()

	r.Group(func(protected chi.Router) {
		protected.Use(middleware.Auth)
		protected.Post("/generate", h.CreateEnrollToken)
	})

	r.Post("/enroll", h.EnrollNode)

	return r
}

type enrollTokenResponse struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
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
		if err == enrollment.ErrInvalidEnrollTokenData {
			http.Error(w, "invalid token data", http.StatusBadRequest)
			return
		}
		log.Printf("[ERROR] create enroll token: %v", err)
		http.Error(w, "could not create token", http.StatusInternalServerError)
		return
	}

	log.Printf("[INFO] enroll token created for user: %s", claims.UserID)

	// Devolver token plano al usuario (solo esta vez)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(enrollTokenResponse{
		Token:     plainToken,
		ExpiresAt: expiresAt,
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
	}

	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}

	if req.EnrollToken == "" {
		fmt.Println("here", req.EnrollToken)
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

	if err := h.service.MarkTokenAsUsed(enroll.Token, time.Now()); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	node := &node.Node{
		Hostname:     req.Hostname,
		IPLocal:      req.IPLocal,
		OS:           req.OS,
		Arch:         req.Arch,
		VersionAgent: req.VersionAgent,
		OwnerID:      &enroll.UserID,
	}

	createdNode, err := h.nodeService.Register(node)
	if err != nil {
		http.Error(w, "failed to create node", http.StatusInternalServerError)
		return
	}

	plainToken, hashToken, err := platform.GenerateTokenApi()
	if err != nil {
		http.Error(w, "failed to generate api token", http.StatusInternalServerError)
		return
	}

	err = h.nodeTokenService.CreateNodeToken(createdNode.ID, hashToken)
	if err != nil {
		http.Error(w, "failed to save api token", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	json.NewEncoder(w).Encode(map[string]string{
		"node_id":      createdNode.ID,
		"api_token":    plainToken,
		"grpc_address": grpcAddress(),
	})

}

// grpcAddress returns the gRPC address that agents should connect to.
// It reads GRPC_ADDRESS from the environment; if not set it falls back to
// ":<GRPC_PORT>" so local dev still works without extra configuration.
func grpcAddress() string {
	if addr := os.Getenv("GRPC_ADDRESS"); addr != "" {
		return addr
	}
	port := os.Getenv("GRPC_PORT")
	if port == "" {
		port = "9090"
	}
	return ":" + port
}
