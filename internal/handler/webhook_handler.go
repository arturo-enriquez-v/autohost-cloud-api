// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package handler

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/arturo/autohost-cloud-api/internal/domain/job"
	nodecommand "github.com/arturo/autohost-cloud-api/internal/domain/node_command"
	"github.com/arturo/autohost-cloud-api/internal/dispatcher"
	"github.com/go-chi/chi/v5"
)

// WebhookHandler handles incoming webhooks from external services.
type WebhookHandler struct {
	jobService *job.Service
	dispatcher dispatcher.NodeDispatcher
}

func NewWebhookHandler(jobService *job.Service, disp dispatcher.NodeDispatcher) *WebhookHandler {
	return &WebhookHandler{
		jobService: jobService,
		dispatcher: disp,
	}
}

func (h *WebhookHandler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Post("/github", h.GitHub)
	return r
}

// githubPushPayload captures the minimal fields from a GitHub push event.
type githubPushPayload struct {
	Ref        string `json:"ref"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
}

// GitHub handles POST /v1/webhooks/github
//
// Required env vars:
//
//	GITHUB_WEBHOOK_SECRET   — shared secret configured in the GitHub repo
//	GITHUB_WEBHOOK_NODE_ID  — ID of the node that will receive the job
//	GITHUB_WEBHOOK_COMMAND  — command name to dispatch on the node
//
// Optional env vars:
//
//	GITHUB_WEBHOOK_BRANCH   — full ref to filter on (e.g. "refs/heads/main").
//	                          Leave empty to react to every push.
func (h *WebhookHandler) GitHub(w http.ResponseWriter, r *http.Request) {
	secret := os.Getenv("GITHUB_WEBHOOK_SECRET")
	nodeID := os.Getenv("GITHUB_WEBHOOK_NODE_ID")
	command := os.Getenv("GITHUB_WEBHOOK_COMMAND")
	branch := os.Getenv("GITHUB_WEBHOOK_BRANCH")

	if secret == "" || nodeID == "" || command == "" {
		log.Println("[WARN] GitHub webhook not configured (GITHUB_WEBHOOK_SECRET / GITHUB_WEBHOOK_NODE_ID / GITHUB_WEBHOOK_COMMAND)")
		http.Error(w, "webhook not configured", http.StatusServiceUnavailable)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20)) // 1 MB limit
	if err != nil {
		http.Error(w, "cannot read body", http.StatusBadRequest)
		return
	}

	if !verifyGitHubSignature(secret, r.Header.Get("X-Hub-Signature-256"), body) {
		http.Error(w, "invalid signature", http.StatusUnauthorized)
		return
	}

	event := r.Header.Get("X-GitHub-Event")

	switch event {
	case "ping":
		w.WriteHeader(http.StatusOK)
		return
	case "push":
		// handled below
	default:
		// unsupported event — acknowledge and ignore
		w.WriteHeader(http.StatusOK)
		return
	}

	var payload githubPushPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		http.Error(w, "invalid payload", http.StatusBadRequest)
		return
	}

	if branch != "" && payload.Ref != branch {
		log.Printf("[INFO] GitHub webhook: push to %s ignored (expected %s)", payload.Ref, branch)
		w.WriteHeader(http.StatusOK)
		return
	}

	j, err := h.jobService.Dispatch(nodeID, command, nodecommand.CommandTypeDefault)
	if err != nil {
		log.Printf("[ERROR] GitHub webhook: dispatch job: %v", err)
		http.Error(w, "could not dispatch job", http.StatusInternalServerError)
		return
	}

	if err := h.dispatcher.DispatchJob(nodeID, j.ID, j.CommandName, j.CommandType); err != nil {
		log.Printf("[WARN] GitHub webhook: node %s not connected, job %s stays pending: %v", nodeID, j.ID, err)
	}

	log.Printf("[INFO] GitHub webhook: dispatched job %s (command=%s node=%s ref=%s)", j.ID, command, nodeID, payload.Ref)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(map[string]string{"job_id": j.ID})
}

// verifyGitHubSignature validates the X-Hub-Signature-256 header.
func verifyGitHubSignature(secret, signatureHeader string, body []byte) bool {
	const prefix = "sha256="
	if !strings.HasPrefix(signatureHeader, prefix) {
		return false
	}
	sig, err := hex.DecodeString(strings.TrimPrefix(signatureHeader, prefix))
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hmac.Equal(sig, mac.Sum(nil))
}
