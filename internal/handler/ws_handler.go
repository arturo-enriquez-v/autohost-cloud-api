package handler

import (
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/arturo/autohost-cloud-api/internal/domain/job"
	nodecommand "github.com/arturo/autohost-cloud-api/internal/domain/node_command"
	"github.com/arturo/autohost-cloud-api/internal/handler/middleware"
	"github.com/go-chi/chi/v5"
	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

// WSHandler manages WebSocket connections from node agents.
// It also implements NodeDispatcher so the JobHandler can push jobs to nodes.
type WSHandler struct {
	clients        map[string]*Client
	clientsMu      sync.RWMutex
	jobService     *job.Service
	commandService *nodecommand.Service
}

func NewWSHandler(jobService *job.Service, commandService *nodecommand.Service) *WSHandler {
	return &WSHandler{
		clients:        make(map[string]*Client),
		jobService:     jobService,
		commandService: commandService,
	}
}

type Client struct {
	NodeID string
	Conn   *websocket.Conn
	Send   chan []byte
	mu     sync.Mutex
}

// Message is the envelope used for all WebSocket communication.
type Message struct {
	Type      string          `json:"type"`
	Payload   json.RawMessage `json:"payload,omitempty"`
	Timestamp time.Time       `json:"timestamp"`
}

func (h *WSHandler) Routes(nodeAuthMiddleware func(http.Handler) http.Handler) http.Handler {
	r := chi.NewRouter()
	r.Group(func(protected chi.Router) {
		protected.Use(nodeAuthMiddleware)
		protected.Get("/ws", h.HandleWebSocket)
	})
	return r
}

func (h *WSHandler) HandleWebSocket(w http.ResponseWriter, r *http.Request) {
	nodeToken := middleware.GetNodeToken(r.Context())
	if nodeToken == nil || nodeToken.NodeID == "" {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Printf("Error upgrading connection: %v", err)
		return
	}

	client := &Client{
		NodeID: nodeToken.NodeID,
		Conn:   conn,
		Send:   make(chan []byte, 256),
	}

	h.registerClient(client)
	defer h.unregisterClient(client)

	welcomeMsg := Message{Type: "connected", Timestamp: time.Now()}
	if err := client.WriteJSON(welcomeMsg); err != nil {
		log.Printf("Error sending welcome message: %v", err)
		return
	}

	go client.writePump()
	client.readPump(h)
}

// ─── NodeDispatcher interface ────────────────────────────────────────────────

// wsExecuteJobPayload is the JSON body inside a WebSocket "execute_job" message.
type wsExecuteJobPayload struct {
	JobID       string                  `json:"job_id"`
	CommandName string                  `json:"command_name"`
	CommandType nodecommand.CommandType `json:"command_type"`
}

// DispatchJob implements handler.NodeDispatcher: serializes an execute_job
// WebSocket message and queues it for the target node.
func (h *WSHandler) DispatchJob(nodeID, jobID, commandName string, commandType nodecommand.CommandType) error {
	payload, err := json.Marshal(wsExecuteJobPayload{
		JobID:       jobID,
		CommandName: commandName,
		CommandType: commandType,
	})
	if err != nil {
		return err
	}
	return h.SendToNode(nodeID, Message{
		Type:      "execute_job",
		Payload:   payload,
		Timestamp: time.Now(),
	})
}

// SendToNode marshals msg and queues it for the named node client.
// Returns an error if the node is not currently connected.
func (h *WSHandler) SendToNode(nodeID string, msg interface{}) error {
	h.clientsMu.RLock()
	client, ok := h.clients[nodeID]
	h.clientsMu.RUnlock()
	if !ok {
		return websocket.ErrCloseSent
	}

	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}

	select {
	case client.Send <- data:
		return nil
	default:
		return websocket.ErrCloseSent
	}
}

// ─── Client registry ─────────────────────────────────────────────────────────

func (h *WSHandler) registerClient(client *Client) {
	h.clientsMu.Lock()
	defer h.clientsMu.Unlock()
	h.clients[client.NodeID] = client
	log.Printf("Node connected: %s (total: %d)", client.NodeID, len(h.clients))
}

func (h *WSHandler) unregisterClient(client *Client) {
	h.clientsMu.Lock()
	defer h.clientsMu.Unlock()
	if _, ok := h.clients[client.NodeID]; ok {
		delete(h.clients, client.NodeID)
		close(client.Send)
		log.Printf("Node disconnected: %s (total: %d)", client.NodeID, len(h.clients))
	}
}

// ─── Message handling ─────────────────────────────────────────────────────────

// jobResultPayload is sent by the node when a job finishes.
type jobResultPayload struct {
	JobID  string `json:"job_id"`
	Status string `json:"status"` // "running" | "completed" | "failed"
	Output string `json:"output"`
	Error  string `json:"error"`
}

// registerCommandPayload is sent by the node when it discovers commands.
type registerCommandPayload struct {
	Name        string                  `json:"name"`
	Description string                  `json:"description"`
	Type        nodecommand.CommandType `json:"type"`
	ScriptPath  string                  `json:"script_path,omitempty"`
}

func (h *WSHandler) handleMessage(c *Client, msg Message) {
	switch msg.Type {
	case "ping":
		_ = c.WriteJSON(Message{Type: "pong", Timestamp: time.Now()})

	case "job_result":
		var p jobResultPayload
		if err := json.Unmarshal(msg.Payload, &p); err != nil {
			log.Printf("[WARN] invalid job_result payload from %s: %v", c.NodeID, err)
			return
		}
		if err := h.jobService.UpdateResult(
			p.JobID,
			job.JobStatus(p.Status),
			p.Output,
			p.Error,
		); err != nil {
			log.Printf("[ERROR] update job %s result: %v", p.JobID, err)
		} else {
			log.Printf("Job %s -> %s (node %s)", p.JobID, p.Status, c.NodeID)
		}

	case "register_command":
		// The agent can also register commands over the WS connection.
		var p registerCommandPayload
		if err := json.Unmarshal(msg.Payload, &p); err != nil {
			log.Printf("[WARN] invalid register_command payload from %s: %v", c.NodeID, err)
			return
		}
		cmd := &nodecommand.NodeCommand{
			NodeID:      c.NodeID,
			Name:        p.Name,
			Description: p.Description,
			Type:        p.Type,
			ScriptPath:  p.ScriptPath,
		}
		if _, err := h.commandService.Register(cmd); err != nil {
			log.Printf("[ERROR] register command %s for node %s: %v", p.Name, c.NodeID, err)
		} else {
			log.Printf("Command '%s' (%s) registered for node %s", p.Name, p.Type, c.NodeID)
		}

	default:
		log.Printf("Unknown message type '%s' from node %s", msg.Type, c.NodeID)
	}
}

// ─── Read / write pumps ───────────────────────────────────────────────────────

func (c *Client) readPump(handler *WSHandler) {
	defer c.Conn.Close()

	c.Conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	c.Conn.SetPongHandler(func(string) error {
		c.Conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})

	for {
		var msg Message
		if err := c.Conn.ReadJSON(&msg); err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				log.Printf("WebSocket error (node %s): %v", c.NodeID, err)
			}
			break
		}
		msg.Timestamp = time.Now()
		handler.handleMessage(c, msg)
	}
}

func (c *Client) writePump() {
	ticker := time.NewTicker(54 * time.Second)
	defer func() {
		ticker.Stop()
		c.Conn.Close()
	}()

	for {
		select {
		case message, ok := <-c.Send:
			c.Conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if !ok {
				c.Conn.WriteMessage(websocket.CloseMessage, []byte{})
				return
			}
			w, err := c.Conn.NextWriter(websocket.TextMessage)
			if err != nil {
				return
			}
			w.Write(message)
			// Flush any additional queued messages
			n := len(c.Send)
			for i := 0; i < n; i++ {
				w.Write([]byte{'\n'})
				w.Write(<-c.Send)
			}
			if err := w.Close(); err != nil {
				return
			}

		case <-ticker.C:
			c.Conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := c.Conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return
			}
		}
	}
}

func (c *Client) WriteJSON(v interface{}) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.Conn.WriteJSON(v)
}
