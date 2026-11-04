// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/redis/go-redis/v9"

	"github.com/arturo/autohost-cloud-api/internal/core/node"
	grpcserver "github.com/arturo/autohost-cloud-api/internal/grpc"
	"github.com/arturo/autohost-cloud-api/internal/handler/middleware"
)

// NodeTerminalHandler bridges browser WebSocket sessions (xterm.js) to an
// interactive PTY shell on a connected node agent over mTLS gRPC.
type NodeTerminalHandler struct {
	grpc    *grpcserver.NodeAgentServer
	nodeSvc *node.NodeService
	redis   *redis.Client
}

func NewNodeTerminalHandler(grpc *grpcserver.NodeAgentServer, nodeSvc *node.NodeService, redis *redis.Client) *NodeTerminalHandler {
	return &NodeTerminalHandler{
		grpc:    grpc,
		nodeSvc: nodeSvc,
		redis:   redis,
	}
}

func (h *NodeTerminalHandler) Routes() http.Handler {
	r := chi.NewRouter()
	r.Group(func(protected chi.Router) {
		protected.Use(middleware.Auth)
		protected.Get("/{nodeId}", h.HandleTerminalWS)
		protected.Get("/{nodeId}/ws", h.HandleTerminalWS)
	})
	return r
}

func (h *NodeTerminalHandler) NodeScopedRoutes() http.Handler {
	r := chi.NewRouter()
	r.Group(func(protected chi.Router) {
		protected.Use(middleware.Auth)
		protected.Get("/", h.HandleTerminalWS)
		protected.Get("/ws", h.HandleTerminalWS)
	})
	return r
}

type terminalClientMsg struct {
	Type string `json:"type"` // "input" | "resize" | "ping"
	Data string `json:"data,omitempty"`
	Cols uint32 `json:"cols,omitempty"`
	Rows uint32 `json:"rows,omitempty"`
}

type terminalServerMsg struct {
	Type      string `json:"type"` // "connected" | "output" | "exit" | "error" | "pong"
	SessionID string `json:"session_id,omitempty"`
	Data      string `json:"data,omitempty"`
	ExitCode  int32  `json:"exit_code,omitempty"`
	Message   string `json:"message,omitempty"`
}

func (h *NodeTerminalHandler) HandleTerminalWS(w http.ResponseWriter, r *http.Request) {
	claims := middleware.GetClaims(r.Context())
	if claims == nil || claims.UserID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	nodeID := chi.URLParam(r, "nodeId")
	if nodeID == "" {
		http.Error(w, "missing nodeId", http.StatusBadRequest)
		return
	}

	if h.nodeSvc != nil {
		n, err := h.nodeSvc.GetByID(nodeID)
		if err != nil || n == nil {
			http.Error(w, "node not found", http.StatusNotFound)
			return
		}
		if n.OwnerID != nil && *n.OwnerID != claims.UserID {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
	}

	cols := uint32(80)
	rows := uint32(24)
	if cq := r.URL.Query().Get("cols"); cq != "" {
		if v, err := strconv.ParseUint(cq, 10, 32); err == nil && v > 0 {
			cols = uint32(v)
		}
	}
	if rq := r.URL.Query().Get("rows"); rq != "" {
		if v, err := strconv.ParseUint(rq, 10, 32); err == nil && v > 0 {
			rows = uint32(v)
		}
	}
	shell := r.URL.Query().Get("shell")
	cwd := r.URL.Query().Get("cwd")
	rawMode := r.URL.Query().Get("raw") == "1"

	// Disable HTTP write timeout for long-lived WebSocket connection
	_ = http.NewResponseController(w).SetWriteDeadline(time.Time{})

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("[Terminal WS] upgrade failed for node %s: %v", nodeID, err)
		return
	}
	defer conn.Close()

	var writeMu sync.Mutex
	writeJSON := func(msg terminalServerMsg) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
		return conn.WriteJSON(msg)
	}
	writeRaw := func(data []byte) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
		return conn.WriteMessage(websocket.BinaryMessage, data)
	}

	sessionID := uuid.NewString()

	// Path 1: Direct in-memory gRPC stream (default in Self-Hosted / monolithic mode)
	if h.grpc != nil && h.grpc.IsNodeConnected(nodeID) {
		session, err := h.grpc.StartTerminalSession(nodeID, sessionID, cols, rows, shell, cwd)
		if err != nil {
			_ = writeJSON(terminalServerMsg{
				Type:    "error",
				Message: fmt.Sprintf("could not start terminal on node: %v", err),
			})
			return
		}
		defer h.grpc.CloseTerminalSession(nodeID, sessionID)

		log.Printf("[Terminal WS] session %s started on node %s (%dx%d)", sessionID, nodeID, cols, rows)
		_ = writeJSON(terminalServerMsg{
			Type:      "connected",
			SessionID: sessionID,
		})

		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()

		// Goroutine: forward PTY output from agent -> browser WebSocket
		go func() {
			pingTicker := time.NewTicker(20 * time.Second)
			defer pingTicker.Stop()

			for {
				select {
				case <-ctx.Done():
					return
				case chunk, ok := <-session.OutputCh:
					if !ok {
						return
					}
					var writeErr error
					if rawMode {
						writeErr = writeRaw(chunk)
					} else {
						writeErr = writeJSON(terminalServerMsg{
							Type: "output",
							Data: string(chunk),
						})
					}
					if writeErr != nil {
						cancel()
						return
					}
				case closedEvt := <-session.ClosedCh:
					if closedEvt != nil {
						_ = writeJSON(terminalServerMsg{
							Type:     "exit",
							ExitCode: closedEvt.GetExitCode(),
							Message:  closedEvt.GetError(),
						})
					}
					cancel()
					return
				case <-session.Done():
					cancel()
					return
				case <-pingTicker.C:
					writeMu.Lock()
					_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
					err := conn.WriteMessage(websocket.PingMessage, nil)
					writeMu.Unlock()
					if err != nil {
						cancel()
						return
					}
				}
			}
		}()

		// Main loop: read input / resize events from browser WebSocket -> agent PTY
		for {
			msgType, rawBytes, err := conn.ReadMessage()
			if err != nil {
				break
			}

			if msgType == websocket.BinaryMessage {
				if err := h.grpc.SendTerminalInput(nodeID, sessionID, rawBytes); err != nil {
					break
				}
				continue
			}

			var clientMsg terminalClientMsg
			if err := json.Unmarshal(rawBytes, &clientMsg); err == nil && clientMsg.Type != "" {
				switch clientMsg.Type {
				case "input":
					if len(clientMsg.Data) > 0 {
						_ = h.grpc.SendTerminalInput(nodeID, sessionID, []byte(clientMsg.Data))
					}
				case "resize":
					if clientMsg.Cols > 0 && clientMsg.Rows > 0 {
						_ = h.grpc.ResizeTerminalSession(nodeID, sessionID, clientMsg.Cols, clientMsg.Rows)
					}
				case "ping":
					_ = writeJSON(terminalServerMsg{Type: "pong"})
				}
			} else {
				// Treat plain text frame as raw terminal input
				_ = h.grpc.SendTerminalInput(nodeID, sessionID, rawBytes)
			}
		}

		log.Printf("[Terminal WS] session %s closed on node %s", sessionID, nodeID)
		return
	}

	// Path 2: Decoupled Redis Pub/Sub bridge (when HTTP and gRPC run in separate processes)
	if h.redis != nil {
		isOnline, _ := h.redis.Exists(r.Context(), fmt.Sprintf("nodes:%s:online", nodeID)).Result()
		if isOnline == 0 {
			_ = writeJSON(terminalServerMsg{
				Type:    "error",
				Message: "node is offline or not connected via gRPC",
			})
			return
		}

		outChannel := fmt.Sprintf("nodes:%s:terminal:%s:out", nodeID, sessionID)
		cmdChannel := fmt.Sprintf("nodes:%s:terminal:cmd", nodeID)

		pubsub := h.redis.Subscribe(r.Context(), outChannel)
		defer pubsub.Close()
		redisCh := pubsub.Channel()

		publishCmd := func(payload map[string]any) {
			data, _ := json.Marshal(payload)
			_ = h.redis.Publish(context.Background(), cmdChannel, data).Err()
		}

		publishCmd(map[string]any{
			"action":     "start",
			"session_id": sessionID,
			"cols":       cols,
			"rows":       rows,
			"shell":      shell,
			"cwd":        cwd,
		})
		defer publishCmd(map[string]any{
			"action":     "close",
			"session_id": sessionID,
		})

		_ = writeJSON(terminalServerMsg{
			Type:      "connected",
			SessionID: sessionID,
		})

		ctx, cancel := context.WithCancel(r.Context())
		defer cancel()

		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case msg, ok := <-redisCh:
					if !ok {
						return
					}
					var evt struct {
						Type     string `json:"type"`
						Data     []byte `json:"data"`
						ExitCode int32  `json:"exit_code"`
						Error    string `json:"error"`
					}
					if err := json.Unmarshal([]byte(msg.Payload), &evt); err != nil {
						continue
					}
					if evt.Type == "output" {
						if rawMode {
							_ = writeRaw(evt.Data)
						} else {
							_ = writeJSON(terminalServerMsg{
								Type: "output",
								Data: string(evt.Data),
							})
						}
					} else if evt.Type == "closed" {
						_ = writeJSON(terminalServerMsg{
							Type:     "exit",
							ExitCode: evt.ExitCode,
							Message:  evt.Error,
						})
						cancel()
						return
					}
				}
			}
		}()

		for {
			msgType, rawBytes, err := conn.ReadMessage()
			if err != nil {
				break
			}
			if msgType == websocket.BinaryMessage {
				publishCmd(map[string]any{
					"action":     "input",
					"session_id": sessionID,
					"data":       rawBytes,
				})
				continue
			}

			var clientMsg terminalClientMsg
			if err := json.Unmarshal(rawBytes, &clientMsg); err == nil && clientMsg.Type != "" {
				switch clientMsg.Type {
				case "input":
					publishCmd(map[string]any{
						"action":     "input",
						"session_id": sessionID,
						"data":       []byte(clientMsg.Data),
					})
				case "resize":
					publishCmd(map[string]any{
						"action":     "resize",
						"session_id": sessionID,
						"cols":       clientMsg.Cols,
						"rows":       clientMsg.Rows,
					})
				case "ping":
					_ = writeJSON(terminalServerMsg{Type: "pong"})
				}
			} else {
				publishCmd(map[string]any{
					"action":     "input",
					"session_id": sessionID,
					"data":       rawBytes,
				})
			}
		}
		return
	}

	_ = writeJSON(terminalServerMsg{
		Type:    "error",
		Message: "node is offline or not connected via gRPC",
	})
}
