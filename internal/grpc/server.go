package grpcserver

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/arturo/autohost-cloud-api/internal/domain/job"
	"github.com/arturo/autohost-cloud-api/internal/domain/node"
	nodealert "github.com/arturo/autohost-cloud-api/internal/domain/node_alert"
	nodecommand "github.com/arturo/autohost-cloud-api/internal/domain/node_command"
	nodetoken "github.com/arturo/autohost-cloud-api/internal/domain/node_token"
	pb "github.com/arturo/autohost-cloud-api/internal/grpc/nodepb"
	"github.com/arturo/autohost-cloud-api/internal/platform"
)

// nodeStream holds the send side of an active gRPC Connect stream.
type nodeStream struct {
	send chan *pb.ServerMessage
}

// logSubscriber receives fan-out log entries for a node.
type logSubscriber struct {
	ch chan *pb.LogEntryPayload
}

// logStreamParams records the active StreamLogs request for a node so it can
// be re-issued automatically when the agent reconnects.
type logStreamParams struct {
	unit  string
	lines int32
}

// NodeAgentServer implements pb.NodeAgentServiceServer.
type NodeAgentServer struct {
	pb.UnimplementedNodeAgentServiceServer

	commandSvc *nodecommand.Service
	jobSvc     *job.Service
	tokenSvc   *nodetoken.Service
	alertSvc   *nodealert.Service
	nodeSvc    *node.Service

	streamsMu sync.RWMutex
	streams   map[string]*nodeStream // nodeID -> active stream

	logSubsMu sync.RWMutex
	logSubs   map[string]map[uint64]*logSubscriber // nodeID -> subID -> subscriber
	logParams map[string]*logStreamParams          // nodeID -> active stream params
	nextSubID uint64
}

// NewNodeAgentServer creates a ready-to-register gRPC server.
func NewNodeAgentServer(
	commandSvc *nodecommand.Service,
	jobSvc *job.Service,
	tokenSvc *nodetoken.Service,
	alertSvc *nodealert.Service,
	nodeSvc *node.Service,
) *NodeAgentServer {
	return &NodeAgentServer{
		commandSvc: commandSvc,
		jobSvc:     jobSvc,
		tokenSvc:   tokenSvc,
		alertSvc:   alertSvc,
		nodeSvc:    nodeSvc,
		streams:    make(map[string]*nodeStream),
		logSubs:    make(map[string]map[uint64]*logSubscriber),
		logParams:  make(map[string]*logStreamParams),
	}
}

// ---- Auth helper ------------------------------------------------------------

// nodeIDFromCtx validates the "authorization" metadata and returns the node ID.
func (s *NodeAgentServer) nodeIDFromCtx(ctx context.Context) (string, error) {
	md, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return "", status.Error(codes.Unauthenticated, "missing metadata")
	}
	values := md.Get("authorization")
	if len(values) == 0 {
		return "", status.Error(codes.Unauthenticated, "missing authorization header")
	}

	raw := values[0]
	if strings.HasPrefix(raw, "Bearer ") {
		raw = raw[7:]
	}

	if !strings.HasPrefix(raw, platform.TokenApiPrefix) {
		return "", status.Error(codes.Unauthenticated, "invalid node token format")
	}

	tokenHash := platform.HashTokenApi(raw)
	tok, err := s.tokenSvc.FindNodeTokenByHash(tokenHash)
	if err != nil {
		return "", status.Error(codes.Unauthenticated, "invalid node token")
	}
	if tok.RevokedAt != nil {
		return "", status.Error(codes.Unauthenticated, "node token revoked")
	}
	return tok.NodeID, nil
}

// ---- NodeDispatcher (used by JobHandler) ------------------------------------

// SendToNode pushes a ServerMessage to a connected node.
// Returns an error if the node has no live gRPC Connect stream.
func (s *NodeAgentServer) SendToNode(nodeID string, msg *pb.ServerMessage) error {
	s.streamsMu.RLock()
	ns, ok := s.streams[nodeID]
	s.streamsMu.RUnlock()
	if !ok {
		return status.Errorf(codes.NotFound, "node %s not connected via gRPC", nodeID)
	}
	select {
	case ns.send <- msg:
		return nil
	default:
		return status.Errorf(codes.Unavailable, "node %s send buffer full", nodeID)
	}
}

// IsNodeConnected reports whether the node has an active gRPC Connect stream.
func (s *NodeAgentServer) IsNodeConnected(nodeID string) bool {
	s.streamsMu.RLock()
	_, ok := s.streams[nodeID]
	s.streamsMu.RUnlock()
	return ok
}

// ---- RegisterCommands (client-side stream) ----------------------------------

func (s *NodeAgentServer) RegisterCommands(
	stream pb.NodeAgentService_RegisterCommandsServer,
) error {
	nodeID, err := s.nodeIDFromCtx(stream.Context())
	if err != nil {
		return err
	}

	var count int32
	for {
		req, recvErr := stream.Recv()
		if recvErr != nil {
			break // io.EOF or disconnect
		}
		cmd := &nodecommand.NodeCommand{
			NodeID:      nodeID,
			Name:        req.GetName(),
			Description: req.GetDescription(),
			Type:        pbCommandType(req.GetType()),
			ScriptPath:  req.GetScriptPath(),
		}
		if _, err := s.commandSvc.Register(cmd); err != nil {
			log.Printf("[gRPC] register command %s for node %s: %v", cmd.Name, nodeID, err)
			continue
		}
		count++
	}

	log.Printf("[gRPC] node %s registered %d commands", nodeID, count)
	return stream.SendAndClose(&pb.RegisterCommandsResponse{Registered: count})
}

// ---- Connect (bidirectional stream) -----------------------------------------

func (s *NodeAgentServer) Connect(
	stream pb.NodeAgentService_ConnectServer,
) error {
	nodeID, err := s.nodeIDFromCtx(stream.Context())
	if err != nil {
		return err
	}

	ns := &nodeStream{send: make(chan *pb.ServerMessage, 64)}
	s.register(nodeID, ns)
	defer func() {
		s.unregister(nodeID)
		close(ns.send)
		log.Printf("[gRPC] node %s disconnected", nodeID)
		go s.sendOfflineAlert(nodeID)
	}()

	log.Printf("[gRPC] node %s connected", nodeID)

	// If there are active SSE subscribers from before a disconnect, re-issue
	// the StreamLogs command so the agent starts sending again.
	s.logSubsMu.RLock()
	params, hasParams := s.logParams[nodeID]
	hasSubs := len(s.logSubs[nodeID]) > 0
	s.logSubsMu.RUnlock()
	if hasParams && hasSubs {
		if err := s.StartNodeLogStream(nodeID, params.unit, params.lines); err != nil {
			log.Printf("[gRPC] re-start log stream for node %s: %v", nodeID, err)
		} else {
			log.Printf("[gRPC] re-issued StreamLogs to reconnected node %s", nodeID)
		}
	}

	// Forward queued ServerMessages to the stream.
	sendErr := make(chan error, 1)
	go func() {
		for msg := range ns.send {
			if err := stream.Send(msg); err != nil {
				sendErr <- err
				return
			}
		}
		sendErr <- nil
	}()

	for {
		// Check if the send goroutine hit an error.
		select {
		case err := <-sendErr:
			return err
		default:
		}

		in, recvErr := stream.Recv()
		if recvErr != nil {
			break
		}

		switch p := in.Payload.(type) {
		case *pb.NodeMessage_JobResult:
			r := p.JobResult
			if err := s.jobSvc.UpdateResult(
				r.GetJobId(),
				job.JobStatus(pbJobStatus(r.GetStatus())),
				r.GetOutput(),
				r.GetError(),
			); err != nil {
				log.Printf("[gRPC] update job %s: %v", r.GetJobId(), err)
			} else {
				log.Printf("[gRPC] job %s -> %s (node %s)", r.GetJobId(), r.GetStatus(), nodeID)
			}

		case *pb.NodeMessage_Heartbeat:
			log.Printf("[gRPC] heartbeat from node %s", p.Heartbeat.GetNodeId())

		case *pb.NodeMessage_LogEntry:
			s.fanOutLogEntry(nodeID, p.LogEntry)

		default:
			log.Printf("[gRPC] unknown payload from node %s", nodeID)
		}
	}

	return nil
}

// ---- Offline alert ----------------------------------------------------------

// sendOfflineAlert waits briefly to see if the node reconnects, then sends a
// Telegram notification if it remains offline and the alert is configured.
func (s *NodeAgentServer) sendOfflineAlert(nodeID string) {
	// Give the node 30 s to reconnect before firing the alert.
	time.Sleep(30 * time.Second)
	if s.IsNodeConnected(nodeID) {
		return // node came back — no alert needed
	}
	if s.alertSvc == nil {
		return
	}
	cfg, err := s.alertSvc.FindByNodeID(nodeID)
	if err != nil || !cfg.AlertOnOffline || cfg.TelegramBotToken == "" || cfg.TelegramChatID == "" {
		return
	}
	nodeName := nodeID
	if s.nodeSvc != nil {
		if n, err := s.nodeSvc.GetByID(nodeID); err == nil {
			nodeName = n.Hostname
		}
	}
	msg := fmt.Sprintf("🔴 <b>AutoHost Alert</b>\nNode <b>%s</b> <code>%s</code> went <b>offline</b>.", nodeName, nodeID)
	if err := platform.SendTelegramMessage(cfg.TelegramBotToken, cfg.TelegramChatID, msg); err != nil {
		log.Printf("[alert] telegram offline alert for node %s: %v", nodeID, err)
	}
}

// ---- Registry ---------------------------------------------------------------

func (s *NodeAgentServer) register(nodeID string, ns *nodeStream) {
	s.streamsMu.Lock()
	defer s.streamsMu.Unlock()
	s.streams[nodeID] = ns
}

func (s *NodeAgentServer) unregister(nodeID string) {
	s.streamsMu.Lock()
	defer s.streamsMu.Unlock()
	delete(s.streams, nodeID)
}

// ---- Proto enum converters --------------------------------------------------

func pbCommandType(t pb.CommandType) nodecommand.CommandType {
	if t == pb.CommandType_COMMAND_TYPE_CUSTOM {
		return nodecommand.CommandTypeCustom
	}
	return nodecommand.CommandTypeDefault
}

func pbJobStatus(s pb.JobStatus) string {
	switch s {
	case pb.JobStatus_JOB_STATUS_RUNNING:
		return string(job.StatusRunning)
	case pb.JobStatus_JOB_STATUS_FAILED:
		return string(job.StatusFailed)
	default:
		return string(job.StatusCompleted)
	}
}

// ---- DispatchJob (implements handler.NodeDispatcher) -----------------------

// DispatchJob builds a gRPC ServerMessage and pushes it to the connected node.
func (s *NodeAgentServer) DispatchJob(
	nodeID, jobID, commandName string,
	commandType nodecommand.CommandType,
) error {
	ct := pb.CommandType_COMMAND_TYPE_DEFAULT
	if commandType == nodecommand.CommandTypeCustom {
		ct = pb.CommandType_COMMAND_TYPE_CUSTOM
	}
	msg := &pb.ServerMessage{
		Payload: &pb.ServerMessage_ExecuteJob{
			ExecuteJob: &pb.ExecuteJobPayload{
				JobId:       jobID,
				CommandName: commandName,
				CommandType: ct,
			},
		},
	}
	return s.SendToNode(nodeID, msg)
}

// ─── Log streaming ────────────────────────────────────────────────────────────

// StartNodeLogStream sends a StreamLogs command to the agent so it starts
// sending LogEntry messages back.  unit="" means all units.
func (s *NodeAgentServer) StartNodeLogStream(nodeID, unit string, lines int32) error {
	return s.SendToNode(nodeID, &pb.ServerMessage{
		Payload: &pb.ServerMessage_StreamLogs{
			StreamLogs: &pb.StreamLogsPayload{Unit: unit, Lines: lines},
		},
	})
}

// StopNodeLogStream asks the agent to stop streaming logs and drops all
// in-process subscribers for that node.
func (s *NodeAgentServer) StopNodeLogStream(nodeID string) error {
	s.logSubsMu.Lock()
	delete(s.logSubs, nodeID)
	s.logSubsMu.Unlock()

	return s.SendToNode(nodeID, &pb.ServerMessage{
		Payload: &pb.ServerMessage_StopLogs{
			StopLogs: &pb.StopLogsPayload{},
		},
	})
}

// SubscribeToNodeLogs returns a channel that receives log entries from the
// given node and a cancel function the caller must invoke when done.
// If this is the first subscriber, it also asks the agent to start streaming.
func (s *NodeAgentServer) SubscribeToNodeLogs(nodeID, unit string, lines int32) (<-chan *pb.LogEntryPayload, func()) {
	id := atomic.AddUint64(&s.nextSubID, 1)
	ch := make(chan *pb.LogEntryPayload, 256)

	s.logSubsMu.Lock()
	if s.logSubs[nodeID] == nil {
		s.logSubs[nodeID] = make(map[uint64]*logSubscriber)
	}
	first := len(s.logSubs[nodeID]) == 0
	s.logSubs[nodeID][id] = &logSubscriber{ch: ch}
	if first {
		s.logParams[nodeID] = &logStreamParams{unit: unit, lines: lines}
	}
	s.logSubsMu.Unlock()

	if first {
		if err := s.StartNodeLogStream(nodeID, unit, lines); err != nil {
			log.Printf("[gRPC] start log stream for node %s: %v", nodeID, err)
		}
	}

	cancel := func() {
		s.logSubsMu.Lock()
		delete(s.logSubs[nodeID], id)
		last := len(s.logSubs[nodeID]) == 0
		if last {
			delete(s.logSubs, nodeID)
			delete(s.logParams, nodeID)
		}
		s.logSubsMu.Unlock()
		close(ch)

		if last {
			if err := s.StopNodeLogStream(nodeID); err != nil {
				log.Printf("[gRPC] stop log stream for node %s: %v", nodeID, err)
			}
		}
	}
	return ch, cancel
}

// fanOutLogEntry delivers a log entry to every subscriber of a node.
func (s *NodeAgentServer) fanOutLogEntry(nodeID string, entry *pb.LogEntryPayload) {
	s.logSubsMu.RLock()
	subs := s.logSubs[nodeID]
	s.logSubsMu.RUnlock()
	for _, sub := range subs {
		select {
		case sub.ch <- entry:
		default: // drop if subscriber is slow
		}
	}
}
