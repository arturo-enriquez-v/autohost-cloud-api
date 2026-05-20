package grpcserver

import (
	"context"
	"io"
	"log"
	"sync"
	"time"

	alertevent "github.com/arturo/autohost-cloud-api/internal/domain/alert_event"
	"github.com/arturo/autohost-cloud-api/internal/domain/job"
	"github.com/arturo/autohost-cloud-api/internal/domain/marketplace"
	"github.com/arturo/autohost-cloud-api/internal/domain/node"
	nodealert "github.com/arturo/autohost-cloud-api/internal/domain/node_alert"
	nodecommand "github.com/arturo/autohost-cloud-api/internal/domain/node_command"
	nodemetric "github.com/arturo/autohost-cloud-api/internal/domain/node_metric"
	nodetoken "github.com/arturo/autohost-cloud-api/internal/domain/node_token"
	sm "github.com/arturo/autohost-cloud-api/internal/domain/service_monitor"
	pb "github.com/arturo/autohost-cloud-api/internal/grpc/nodepb"
	"github.com/arturo/autohost-cloud-api/internal/platform"
)

// NodeAgentServer implements pb.NodeAgentServiceServer.
type NodeAgentServer struct {
	pb.UnimplementedNodeAgentServiceServer

	commandSvc        *nodecommand.Service
	jobSvc            *job.Service
	tokenSvc          *nodetoken.Service
	alertSvc          *nodealert.Service
	alertEventSvc     *alertevent.Service
	nodeSvc           *node.Service
	metricSvc         *nodemetric.Service
	marketplaceRepo   marketplace.Repository
	serviceMonitorSvc *sm.Service

	registry      *streamRegistry
	logs          *logBroker
	metricBroker  *metricBroker
	profileBroker *profileBroker

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
	marketplaceRepo marketplace.Repository,
	alertEventSvc *alertevent.Service,
	serviceMonitorSvc *sm.Service,
) *NodeAgentServer {
	reg := newStreamRegistry()
	s := &NodeAgentServer{
		commandSvc:        commandSvc,
		jobSvc:            jobSvc,
		tokenSvc:          tokenSvc,
		alertSvc:          alertSvc,
		alertEventSvc:     alertEventSvc,
		nodeSvc:           nodeSvc,
		metricSvc:         metricSvc,
		registry:          reg,
		marketplaceRepo:   marketplaceRepo,
		serviceMonitorSvc: serviceMonitorSvc,
		metricBroker:      newMetricBroker(),
		profileBroker:     newProfileBroker(),
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

	// Re-send all enabled health check configs so the agent resumes monitoring
	// after a reconnect (agent discards all loops on disconnect).
	if s.serviceMonitorSvc != nil {
		if monitors, err := s.serviceMonitorSvc.ListByNodeID(nodeID); err != nil {
			log.Printf("[gRPC] list monitors for node %s on reconnect: %v", nodeID, err)
		} else {
			for _, m := range monitors {
				if m.Enabled {
					s.SendHealthCheckConfig(nodeID, m)
				}
			}
			if len(monitors) > 0 {
				log.Printf("[gRPC] re-sent %d health check config(s) to reconnected node %s", len(monitors), nodeID)
			}
		}
	}

	// Re-dispatch pending install_app jobs so the agent receives params it may
	// have missed while it was offline.
	if pendingJobs, err := s.jobSvc.FindPendingByNodeID(nodeID, nodecommand.CommandTypeInstallApp); err != nil {
		log.Printf("[gRPC] list pending install_app jobs for node %s: %v", nodeID, err)
	} else {
		for _, j := range pendingJobs {
			if j.Params == "" {
				continue // no params to send; skip
			}
			if err := s.DispatchJobWithParams(nodeID, j.ID, j.CommandName, j.CommandType, j.Params); err != nil {
				log.Printf("[gRPC] re-dispatch pending job %s to node %s: %v", j.ID, nodeID, err)
			} else {
				log.Printf("[gRPC] re-dispatched pending job %s (%s) to reconnected node %s", j.ID, j.CommandName, nodeID)
			}
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
			jobStatus := job.JobStatus(pbJobStatus(r.GetStatus()))
			if err := s.jobSvc.UpdateResult(
				r.GetJobId(),
				jobStatus,
				r.GetOutput(),
				r.GetError(),
			); err != nil {
				log.Printf("[gRPC] update job %s: %v", r.GetJobId(), err)
			} else {
				log.Printf("[gRPC] job %s -> %s (node %s)", r.GetJobId(), r.GetStatus(), nodeID)
			}

			// Sync marketplace_apps status when an install job finishes.
			if s.marketplaceRepo != nil {
				if mApp, err := s.marketplaceRepo.FindByJobID(r.GetJobId()); err == nil {
					var appStatus marketplace.AppStatus
					if jobStatus == job.StatusCompleted {
						appStatus = marketplace.StatusRunning
					} else {
						appStatus = marketplace.StatusFailed
					}
					if err := s.marketplaceRepo.UpdateStatus(mApp.ID, appStatus); err != nil {
						log.Printf("[gRPC] update marketplace app %s status: %v", mApp.ID, err)
					}
				}
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
				// Check alert thresholds asynchronously; never block the stream.
				go s.checkThresholdAlerts(nodeID, req)
			}

		case *pb.NodeMessage_LogEntry:
			s.logs.fanOut(nodeID, p.LogEntry)

		case *pb.NodeMessage_HealthResult:
			go s.handleHealthCheckResult(nodeID, p.HealthResult)

		case *pb.NodeMessage_ProfileData:
			// Route the raw pprof bytes to the HTTP handler that is waiting.
			s.profileBroker.deliver(p.ProfileData)

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

// ---- Health Check helpers ---------------------------------------------------

// SendHealthCheckConfig pushes a monitor configuration to a connected agent.
func (s *NodeAgentServer) SendHealthCheckConfig(nodeID string, m *sm.ServiceMonitor) {
	msg := &pb.ServerMessage{
		Payload: &pb.ServerMessage_ConfigureHealthCheck{
			ConfigureHealthCheck: &pb.ConfigureHealthCheckPayload{
				MonitorId:          m.ID,
				ServiceName:        m.ServiceName,
				CheckType:          m.CheckType,
				HttpUrl:            m.HTTPURL,
				HttpExpectedStatus: int32(m.HTTPExpectedStatus),
				TcpHost:            m.TCPHost,
				TcpPort:            int32(m.TCPPort),
				IntervalSeconds:    int32(m.IntervalSeconds),
				FailureThreshold:   int32(m.FailureThreshold),
				Enabled:            m.Enabled,
			},
		},
	}
	if err := s.registry.sendToNode(nodeID, msg); err != nil {
		log.Printf("[gRPC] SendHealthCheckConfig node %s monitor %s: %v", nodeID, m.ID, err)
	}
}

// StopHealthCheck tells a connected agent to remove the given monitor.
func (s *NodeAgentServer) StopHealthCheck(nodeID, monitorID string) {
	msg := &pb.ServerMessage{
		Payload: &pb.ServerMessage_StopHealthCheck{
			StopHealthCheck: &pb.StopHealthCheckPayload{
				MonitorId: monitorID,
			},
		},
	}
	if err := s.registry.sendToNode(nodeID, msg); err != nil {
		log.Printf("[gRPC] StopHealthCheck node %s monitor %s: %v", nodeID, monitorID, err)
	}
}

// handleHealthCheckResult persists an incoming health check result from the agent.
func (s *NodeAgentServer) handleHealthCheckResult(nodeID string, r *pb.HealthCheckResultPayload) {
	if s.serviceMonitorSvc == nil {
		return
	}

	monitor, err := s.serviceMonitorSvc.FindByID(r.GetMonitorId())
	if err != nil {
		log.Printf("[gRPC] health check result for unknown monitor %s: %v", r.GetMonitorId(), err)
		return
	}

	// Determine new aggregate status.
	newStatus := sm.StatusHealthy
	if r.GetStatus() == "down" {
		if int(r.GetConsecutiveFailures()) >= monitor.FailureThreshold {
			newStatus = sm.StatusDown
		} else {
			newStatus = sm.StatusDegraded
		}
	}

	if err := s.serviceMonitorSvc.UpdateStatus(monitor.ID, newStatus, int(r.GetConsecutiveFailures())); err != nil {
		log.Printf("[gRPC] update service monitor status %s: %v", monitor.ID, err)
	}

	event := &sm.ServiceHealthEvent{
		MonitorID: monitor.ID,
		NodeID:    nodeID,
		UserID:    monitor.UserID,
		Status:    r.GetStatus(),
		LatencyMs: int(r.GetLatencyMs()),
		Message:   r.GetMessage(),
		CheckedAt: time.Now(),
	}
	if err := s.serviceMonitorSvc.RecordEvent(event); err != nil {
		log.Printf("[gRPC] record health event monitor %s: %v", monitor.ID, err)
	}

	// Send Telegram alert on state transition to down.
	if newStatus == sm.StatusDown && monitor.NotifyTelegram {
		go s.sendServiceDownAlert(nodeID, monitor)
	}

	log.Printf("[gRPC] health check %s (%s): %s latency=%dms",
		monitor.ServiceName, monitor.ID, r.GetStatus(), r.GetLatencyMs())
}

// sendServiceDownAlert sends a Telegram notification when a service goes down.
func (s *NodeAgentServer) sendServiceDownAlert(nodeID string, monitor *sm.ServiceMonitor) {
	alert, err := s.alertSvc.FindByNodeID(nodeID)
	if err != nil || alert.TelegramBotToken == "" || alert.TelegramChatID == "" {
		return
	}
	message := "🔴 Service DOWN: " + monitor.ServiceName + " on node " + nodeID
	platform.SendTelegramMessage(alert.TelegramBotToken, alert.TelegramChatID, message) //nolint:errcheck
}
