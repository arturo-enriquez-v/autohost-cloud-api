package handler

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	grpcserver "github.com/arturo/autohost-cloud-api/internal/grpc"
	pb "github.com/arturo/autohost-cloud-api/internal/grpc/nodepb"
	"github.com/arturo/autohost-cloud-api/internal/handler/middleware"
)

// NodeLogHandler streams real-time system logs from a node to the browser via
// Server-Sent Events (SSE).  The client must supply a valid user JWT.
type NodeLogHandler struct {
	grpc *grpcserver.NodeAgentServer
}

func NewNodeLogHandler(grpc *grpcserver.NodeAgentServer) *NodeLogHandler {
	return &NodeLogHandler{grpc: grpc}
}

func (h *NodeLogHandler) Routes() http.Handler {
	r := chi.NewRouter()
	r.Group(func(protected chi.Router) {
		protected.Use(middleware.Auth)
		protected.Get("/{nodeId}/stream", h.streamLogs)
	})
	return r
}

// sseLogEntry is the JSON payload sent on each SSE event.
type sseLogEntry struct {
	TimestampUs int64  `json:"timestamp_us"`
	Unit        string `json:"unit"`
	Message     string `json:"message"`
}

func (h *NodeLogHandler) streamLogs(w http.ResponseWriter, r *http.Request) {
	nodeID := chi.URLParam(r, "nodeId")

	// Optional query params.
	unit := r.URL.Query().Get("unit") // e.g. "autohost-agent" or "" = all
	lines := int32(100)
	if lq := r.URL.Query().Get("lines"); lq != "" {
		if n, err := strconv.Atoi(lq); err == nil && n >= 0 {
			lines = int32(n)
		}
	}

	// SSE headers — disable buffering.
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // nginx passthrough

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	// Remove the server-wide WriteTimeout for this long-lived SSE connection.
	// Without this, net/http kills the stream after WriteTimeout (30 s).
	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})

	// Subscribe — this also asks the agent to start streaming if needed.
	ch, cancel := h.grpc.SubscribeToNodeLogs(nodeID, unit, lines)
	defer cancel()

	log.Printf("[SSE] log stream started for node %s (unit=%q lines=%d)", nodeID, unit, lines)

	// Send an initial keepalive comment so the browser confirms the connection.
	fmt.Fprintf(w, ": connected\n\n")
	flusher.Flush()

	// Verify the node actually has a live gRPC stream.
	if !h.grpc.IsNodeConnected(nodeID) {
		fmt.Fprintf(w, "event: server-error\ndata: {\"message\":\"node not connected via gRPC — is the agent running?\"}\n\n")
		flusher.Flush()
	}

	// Use a Ticker instead of time.After to avoid creating a new timer (and its
	// associated goroutine) on every iteration of the select loop.
	keepalive := time.NewTicker(5 * time.Second)
	defer keepalive.Stop()

	for {
		select {
		case <-r.Context().Done():
			log.Printf("[SSE] log stream closed for node %s", nodeID)
			return

		case entry, ok := <-ch:
			if !ok {
				// Subscription was cancelled from the server side.
				return
			}
			if err := writeSSELogEntry(w, entry); err != nil {
				log.Printf("[SSE] write error for node %s: %v", nodeID, err)
				return
			}
			flusher.Flush()

		case <-keepalive.C:
			// Keepalive comment — also detects a closed client connection within 5 s.
			if _, err := fmt.Fprintf(w, ": ping\n\n"); err != nil {
				log.Printf("[SSE] client disconnected for node %s", nodeID)
				return
			}
			flusher.Flush()
		}
	}
}

func writeSSELogEntry(w http.ResponseWriter, e *pb.LogEntryPayload) error {
	payload := sseLogEntry{
		TimestampUs: e.GetTimestampUs(),
		Unit:        e.GetUnit(),
		Message:     e.GetMessage(),
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "data: %s\n\n", data)
	return err
}
