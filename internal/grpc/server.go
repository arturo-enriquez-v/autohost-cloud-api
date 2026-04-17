package grpcserver

import (
	"context"
	"io"
	"log"
	"sync"
	"time"

	"github.com/arturo/autohost-cloud-api/internal/domain/job"
	"github.com/arturo/autohost-cloud-api/internal/domain/node"
	nodealert "github.com/arturo/autohost-cloud-api/internal/domain/node_alert"
	nodecommand "github.com/arturo/autohost-cloud-api/internal/domain/node_command"
	nodemetric "github.com/arturo/autohost-cloud-api/internal/domain/node_metric"
	nodetoken "github.com/arturo/autohost-cloud-api/internal/domain/node_token"
	pb "github.com/arturo/autohost-cloud-api/internal/grpc/nodepb"
)

// NodeAgentServer implements pb.NodeAgentServiceServer.
type NodeAgentServer struct {
	pb.UnimplementedNodeAgentServiceServer

	commandSvc *nodecommand.Service
	jobSvc     *job.Service
	tokenSvc   *nodetoken.Service
	alertSvc   *nodealert.Service
	nodeSvc    *node.Service
	metricSvc  *nodemetric.Service

	registry     *streamRegistry
	logs         *logBroker
	metricBroker *metricBroker

	// metricPersistMu guards metricLastPersist to throttle DB writes.
	metricPersistMu   sync.Mutex
	metricLastPersist map[string]time.Time // nodeID -> last DB write time
}

// NewNodeAgentServer creates a ready-to-register gRPC server.
func NewNodeAgentServer(
	commandSvc *nodecommand.Service,
	jobSvc *job.Service,
	tokenSvc *nodetoken.Service,
	alertSvc *nodealert.Service,
	nodeSvc *node.Service,
	metricSvc *nodemetric.Service,
) *NodeAgentServer {
	reg := newStreamRegistry()
	s := &NodeAgentServer{
		commandSvc:        commandSvc,
		jobSvc:            jobSvc,
		tokenSvc:          tokenSvc,
		alertSvc:          alertSvc,
		nodeSvc:           nodeSvc,
		metricSvc:         metricSvc,
		registry:          reg,
		metricBroker:      newMetricBroker(),
		metricLastPersist: make(map[string]time.Time),
	}
	s.logs = newLogBroker(reg.sendToNode)
	return s
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
		if recvErr == io.EOF {
			break
		}
		if recvErr != nil {
			return recvErr // propagate as gRPC status error
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
	s.registry.register(nodeID, ns)
	defer func() {
		s.registry.unregister(nodeID)
		close(ns.send)
		log.Printf("[gRPC] node %s disconnected", nodeID)
		go s.sendOfflineAlert(nodeID)
	}()

	log.Printf("[gRPC] node %s connected", nodeID)

	// If there are active SSE subscribers from before a disconnect, re-issue
	// the StreamLogs command so the agent starts sending again.
	if params, ok := s.logs.activeParams(nodeID); ok {
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
			if err := s.nodeSvc.UpdateLastSeen(nodeID); err != nil {
				log.Printf("[gRPC] update last_seen for node %s: %v", nodeID, err)
			} else {
				log.Printf("[gRPC] heartbeat from node %s", p.Heartbeat.GetNodeId())
			}

		case *pb.NodeMessage_Metric:
			m := p.Metric
			// Fan-out to live SSE subscribers immediately — zero DB cost.
			s.metricBroker.fanOut(nodeID, m)
			// Persist to DB at most once per minute to keep write volume low.
			if s.shouldPersistMetric(nodeID) {
				req := &nodemetric.CreateNodeMetricRequest{
					NodeID:               nodeID,
					CPUUsagePercent:      float64(m.GetCpuUsagePercent()),
					MemoryTotalBytes:     int64(m.GetRamTotalBytes()),
					MemoryUsedBytes:      int64(m.GetRamUsedBytes()),
					MemoryAvailableBytes: int64(m.GetRamAvailableBytes()),
					MemoryUsagePercent:   float64(m.GetRamUsagePercent()),
					DiskTotalBytes:       int64(m.GetDiskTotalBytes()),
					DiskUsedBytes:        int64(m.GetDiskUsedBytes()),
					DiskAvailableBytes:   int64(m.GetDiskAvailableBytes()),
					DiskUsagePercent:     float64(m.GetDiskUsagePercent()),
					CollectedAt:          time.Now(),
				}
				if _, err := s.metricSvc.StoreNodeMetric(req); err != nil {
					log.Printf("[gRPC] store metrics for node %s: %v", nodeID, err)
				}
				// Piggyback last_seen update on the same throttled window.
				if err := s.nodeSvc.UpdateLastSeen(nodeID); err != nil {
					log.Printf("[gRPC] update last_seen (metric) for node %s: %v", nodeID, err)
				}
			}

		case *pb.NodeMessage_LogEntry:
			s.logs.fanOut(nodeID, p.LogEntry)

		default:
			log.Printf("[gRPC] unknown payload from node %s", nodeID)
		}
	}

	return nil
}

// ---- ReportMetrics (kept for backward compat — unary) -----------------------

func (s *NodeAgentServer) ReportMetrics(
	ctx context.Context,
	req *pb.MetricRequest,
) (*pb.MetricResponse, error) {
	nodeID, err := s.nodeIDFromCtx(ctx)
	if err != nil {
		return nil, err
	}

	log.Printf("[gRPC] ReportMetrics from node %s: CPU %.2f%%", nodeID, req.GetCpuUsagePercent())
	return &pb.MetricResponse{Success: true}, nil
}

// ---- Live metrics SSE subscription -----------------------------------------

// shouldPersistMetric returns true if enough time has passed since the last
// DB write for nodeID (throttled to once per minute).
func (s *NodeAgentServer) shouldPersistMetric(nodeID string) bool {
	const persistInterval = 60 * time.Second
	now := time.Now()
	s.metricPersistMu.Lock()
	defer s.metricPersistMu.Unlock()
	if last, ok := s.metricLastPersist[nodeID]; ok && now.Sub(last) < persistInterval {
		return false
	}
	s.metricLastPersist[nodeID] = now
	return true
}

// SubscribeToNodeMetrics returns a channel that receives real-time metric
// payloads for the given node, plus a cancel function to unsubscribe.
func (s *NodeAgentServer) SubscribeToNodeMetrics(nodeID string) (<-chan *pb.MetricPayload, func()) {
	return s.metricBroker.subscribe(nodeID)
}
