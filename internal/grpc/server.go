// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package grpcserver

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/arturo/autohost-cloud-api/internal/core/apps"
	"github.com/arturo/autohost-cloud-api/internal/core/identity"
	"github.com/arturo/autohost-cloud-api/internal/core/infra"
	"github.com/arturo/autohost-cloud-api/internal/core/jobs"
	"github.com/arturo/autohost-cloud-api/internal/core/network"
	"github.com/arturo/autohost-cloud-api/internal/core/node"
	pb "github.com/arturo/autohost-cloud-api/internal/grpc/nodepb"
	"github.com/arturo/autohost-cloud-api/internal/platform"
)

// NodeAgentServer implements pb.NodeAgentServiceServer.
type NodeAgentServer struct {
	pb.UnimplementedNodeAgentServiceServer

	authSvc            *identity.AuthService
	notificationSvc    *identity.NotificationService
	serviceMonitorSvc  *apps.ServiceMonitorService
	alertSvc           *node.NodeAlertService
	alertEventSvc      *node.AlertEventService
	marketplaceRepo    apps.MarketplaceRepository
	tokenSvc           *node.NodeTokenService
	commandSvc         *jobs.NodeCommandService
	jobSvc             *jobs.JobService
	nodeSvc            *node.NodeService
	metricSvc          *node.NodeMetricService
	containerMetricSvc *node.ContainerMetricService
	backupSvc          *apps.BackupService
	logDiagnosticSvc   *node.LogDiagnosticService
	networkStatSvc     *network.NetworkStatService
	vmRepo             infra.VMRepository

	registry        *streamRegistry
	logs            *logBroker
	metricBroker    *metricBroker
	containerBroker *containerBroker
	terminalBroker  *terminalBroker

	networkStatsMu      sync.Mutex
	networkStatsPending map[string]chan *networkStatsResponse

	fileOpsMu      sync.Mutex
	fileOpsPending map[string]chan *pb.FileOperationResponsePayload

	// metricPersistMu guards metricLastPersist to throttle DB writes.
	metricPersistMu   sync.Mutex
	metricLastPersist map[string]time.Time // nodeID -> last DB write time

	redis *redis.Client

	// hypervisorInfo caches the latest HypervisorInfo per node (nodeID -> *HypervisorInfo).
	hypervisorInfo sync.Map
}

// NewNodeAgentServer creates a ready-to-register gRPC server.
func NewNodeAgentServer(
	commandSvc *jobs.NodeCommandService,
	jobSvc *jobs.JobService,
	tokenSvc *node.NodeTokenService,
	alertSvc *node.NodeAlertService,
	nodeSvc *node.NodeService,
	metricSvc *node.NodeMetricService,
	containerMetricSvc *node.ContainerMetricService,
	marketplaceRepo apps.MarketplaceRepository,
	alertEventSvc *node.AlertEventService,
	serviceMonitorSvc *apps.ServiceMonitorService,
	authSvc *identity.AuthService,
	notificationSvc *identity.NotificationService,
	backupSvc *apps.BackupService,
	logDiagnosticSvc *node.LogDiagnosticService,
	networkStatSvc *network.NetworkStatService,
	redis *redis.Client,
	vmRepo infra.VMRepository,
) *NodeAgentServer {
	reg := newStreamRegistry()
	s := &NodeAgentServer{
		commandSvc:          commandSvc,
		jobSvc:              jobSvc,
		tokenSvc:            tokenSvc,
		alertSvc:            alertSvc,
		alertEventSvc:       alertEventSvc,
		nodeSvc:             nodeSvc,
		metricSvc:           metricSvc,
		containerMetricSvc:  containerMetricSvc,
		registry:            reg,
		marketplaceRepo:     marketplaceRepo,
		serviceMonitorSvc:   serviceMonitorSvc,
		authSvc:             authSvc,
		notificationSvc:     notificationSvc,
		backupSvc:           backupSvc,
		logDiagnosticSvc:    logDiagnosticSvc,
		networkStatSvc:      networkStatSvc,
		redis:               redis,
		vmRepo:              vmRepo,
		networkStatsPending: make(map[string]chan *networkStatsResponse),
		fileOpsPending:      make(map[string]chan *pb.FileOperationResponsePayload),
		metricBroker:        newMetricBroker(),
		metricLastPersist:   make(map[string]time.Time),
	}
	s.logs = newLogBroker(reg.sendToNode)
	s.containerBroker = newContainerBroker(reg.sendToNode)
	s.terminalBroker = newTerminalBroker(reg.sendToNode)
	if s.redis != nil {
		go s.listenContainerCommands()
	}
	return s
}

func (s *NodeAgentServer) SetBackupService(svc *apps.BackupService) {
	s.backupSvc = svc
}

func (s *NodeAgentServer) SetVMRepository(repo infra.VMRepository) {
	s.vmRepo = repo
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
		cmd := &jobs.NodeCommand{
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
	s.markOnline(nodeID)
	defer func() {
		s.registry.unregister(nodeID, ns)
		close(ns.send)
		if s.terminalBroker != nil {
			s.terminalBroker.closeAllForNode(nodeID)
		}
		if s.redis != nil {
			_ = s.redis.Del(context.Background(), fmt.Sprintf("nodes:%s:online", nodeID)).Err()
		}
		log.Printf("[gRPC] node %s disconnected", nodeID)
		go s.sendOfflineAlert(nodeID)
	}()

	log.Printf("[gRPC] node %s connected", nodeID)
	s.resumeNodeState(nodeID)

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

	// Re-validate the node's enrollment periodically so a long-lived stream is cut shortly after the node
	// is deleted or its tokens are revoked (the TLS client certificate itself stays valid for a year).
	const enrollmentRecheckInterval = time.Minute
	nextEnrollmentCheck := time.Now().Add(enrollmentRecheckInterval)

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

		if time.Now().After(nextEnrollmentCheck) {
			nextEnrollmentCheck = time.Now().Add(enrollmentRecheckInterval)
			active := s.checkEnrollmentUncached(nodeID)
			enrollmentCache.Store(nodeID, enrollmentCacheEntry{active: active, expiresAt: time.Now().Add(enrollmentCacheTTL)})
			if !active {
				log.Printf("[gRPC] closing stream for node %s: node deleted or tokens revoked", nodeID)
				return status.Error(codes.Unauthenticated, "node enrollment revoked")
			}
		}

		s.handleNodeMessage(nodeID, in)
	}

	return nil
}

// resumeNodeState restores what a (re)connected node should be doing: the agent drops all streams and
// health-check loops when it disconnects, and may have missed jobs dispatched while it was offline.
func (s *NodeAgentServer) resumeNodeState(nodeID string) {
	// Re-issue StreamContainers if there are active subscribers waiting.
	if s.containerBroker.hasSubscribers(nodeID) {
		if err := s.containerBroker.startStream(nodeID); err != nil {
			log.Printf("[gRPC] re-start container stream for node %s: %v", nodeID, err)
		} else {
			log.Printf("[gRPC] re-issued StreamContainers to reconnected node %s", nodeID)
		}
	}

	// If there are active SSE subscribers from before a disconnect, re-issue
	// the StreamLogs command so the agent starts sending again.
	if params, ok := s.logs.activeParams(nodeID); ok {
		if err := s.StartNodeLogStream(nodeID, params.unit, params.lines); err != nil {
			log.Printf("[gRPC] re-start log stream for node %s: %v", nodeID, err)
		} else {
			log.Printf("[gRPC] re-issued StreamLogs to reconnected node %s", nodeID)
		}
	} else if s.redis != nil {
		channel := fmt.Sprintf("nodes:%s:logs", nodeID)
		if numMap, err := s.redis.PubSubNumSub(context.Background(), channel).Result(); err == nil && numMap[channel] > 0 {
			unit := ""
			lines := int32(100)
			if p, ok := s.logs.getParams(nodeID); ok && p != nil {
				unit = p.unit
				lines = p.lines
			}
			if err := s.logs.startStream(nodeID, unit, lines); err != nil {
				log.Printf("[gRPC] re-start log stream for node %s via Redis: %v", nodeID, err)
			} else {
				log.Printf("[gRPC] re-issued StreamLogs (Redis) to reconnected node %s", nodeID)
			}
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
	if pendingJobs, err := s.jobSvc.FindPendingByNodeID(nodeID, jobs.CommandTypeInstallApp); err != nil {
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
// DB write for nodeID (throttled to once every 15 s).
// 15 s keeps DB writes low while staying close enough to the 10 s frontend
// polling interval so the nodes-list always shows a fresh value.
func (s *NodeAgentServer) shouldPersistMetric(nodeID string) bool {
	const persistInterval = 15 * time.Second
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
func (s *NodeAgentServer) SendHealthCheckConfig(nodeID string, m *apps.ServiceMonitor) {
	if s == nil || s.registry == nil {
		return
	}
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
				AutoHealConfig:     string(m.AutoHealConfig),
				AutoHealState:      string(m.AutoHealState),
			},
		},
	}
	if err := s.registry.sendToNode(nodeID, msg); err != nil {
		log.Printf("[gRPC] SendHealthCheckConfig node %s monitor %s: %v", nodeID, m.ID, err)
	}
}

// StopHealthCheck tells a connected agent to remove the given monitor.
func (s *NodeAgentServer) StopHealthCheck(nodeID, monitorID string) {
	if s == nil || s.registry == nil {
		return
	}
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
	newStatus := apps.StatusHealthy
	if r.GetStatus() == "down" {
		if int(r.GetConsecutiveFailures()) >= monitor.FailureThreshold {
			newStatus = apps.StatusDown
		} else {
			newStatus = apps.StatusDegraded
		}
	}

	if err := s.serviceMonitorSvc.UpdateStatus(monitor.ID, newStatus, int(r.GetConsecutiveFailures())); err != nil {
		log.Printf("[gRPC] update service monitor status %s: %v", monitor.ID, err)
	}

	if autoHealStateStr := r.GetAutoHealState(); autoHealStateStr != "" && autoHealStateStr != "{}" {
		// Parse old and new state
		var oldState identity.AutoHealState
		if len(monitor.AutoHealState) > 0 && string(monitor.AutoHealState) != "{}" && string(monitor.AutoHealState) != "null" {
			_ = json.Unmarshal(monitor.AutoHealState, &oldState)
		}

		var newState identity.AutoHealState
		if err := json.Unmarshal([]byte(autoHealStateStr), &newState); err == nil {
			var cfg identity.AutoHealConfig
			if len(monitor.AutoHealConfig) > 0 && string(monitor.AutoHealConfig) != "{}" && string(monitor.AutoHealConfig) != "null" {
				_ = json.Unmarshal(monitor.AutoHealConfig, &cfg)
			}

			if cfg.Enabled && s.notificationSvc != nil {
				go func() {
					recipient := ""
					if s.authSvc != nil {
						if u, err := s.authSvc.GetByID(monitor.UserID); err == nil && u != nil {
							recipient = u.Email
						}
					}
					nodeHostname := ""
					nodeIP := ""
					if s.nodeSvc != nil {
						if n, err := s.nodeSvc.GetByID(nodeID); err == nil && n != nil {
							nodeHostname = n.Hostname
							nodeIP = n.IPLocal
							if n.IPVPN != nil && *n.IPVPN != "" {
								nodeIP = *n.IPVPN
							}
						}
					}

					s.notificationSvc.SendAutoHealTransitionAlert(context.Background(), identity.AutoHealAlertContext{
						Recipient:    recipient,
						NodeID:       nodeID,
						NodeHostname: nodeHostname,
						NodeIP:       nodeIP,
						MonitorID:    monitor.ID,
						ServiceName:  monitor.ServiceName,
						CheckType:    monitor.CheckType,
						Config:       cfg,
						OldState:     oldState,
						NewState:     newState,
						LastMessage:  r.GetMessage(),
						LatencyMs:    int(r.GetLatencyMs()),
					})
				}()
			}
		}

		if err := s.serviceMonitorSvc.UpdateAutoHealState(monitor.ID, json.RawMessage(autoHealStateStr)); err != nil {
			log.Printf("[gRPC] update auto heal state %s: %v", monitor.ID, err)
		}
	}

	event := &apps.ServiceHealthEvent{
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

	// The agent reports every check while the service stays down; alert only when it goes down.
	if monitor.NotifyTelegram && wentDown(monitor.Status, newStatus) {
		go s.sendServiceDownAlert(nodeID, monitor, r.GetMessage())
	}

	log.Printf("[gRPC] health check %s (%s): %s latency=%dms",
		monitor.ServiceName, monitor.ID, r.GetStatus(), r.GetLatencyMs())
}

// wentDown reports whether a monitor moved into the down status with this check.
func wentDown(previous, current string) bool {
	return current == apps.StatusDown && previous != apps.StatusDown
}

// sendServiceDownAlert sends a Telegram notification when a service goes down.
func (s *NodeAgentServer) sendServiceDownAlert(nodeID string, monitor *apps.ServiceMonitor, detail string) {
	if s.alertSvc == nil {
		return
	}
	alert, err := s.alertSvc.FindByNodeID(nodeID)
	if err != nil || alert.TelegramBotToken == "" || alert.TelegramChatID == "" {
		return
	}
	nodeLabel := nodeID
	if s.nodeSvc != nil {
		if n, err := s.nodeSvc.GetByID(nodeID); err == nil && n != nil && n.Hostname != "" {
			nodeLabel = n.Hostname
		}
	}
	message := "🔴 Service DOWN: " + monitor.ServiceName + " on " + nodeLabel
	if detail != "" {
		message += "\n" + detail
	}
	platform.SendTelegramMessage(alert.TelegramBotToken, alert.TelegramChatID, message) //nolint:errcheck
}

// listenContainerCommands listens for container/log stream commands and remote jobs published via Redis.
func (s *NodeAgentServer) listenContainerCommands() {
	pubsub := s.redis.PSubscribe(context.Background(), "nodes:*:containers:cmd", "nodes:*:jobs:cmd", "nodes:*:logs:cmd", "nodes:*:terminal:cmd")
	defer pubsub.Close()

	ch := pubsub.Channel()
	for msg := range ch {
		// msg.Channel format: "nodes:<nodeID>:<scope>:cmd"
		parts := strings.Split(msg.Channel, ":")
		if len(parts) >= 4 && parts[0] == "nodes" && parts[3] == "cmd" {
			targetNodeID := parts[1]
			scope := parts[2]

			// Only process messages for nodes connected to THIS gRPC instance.
			// This prevents infinite Redis publish loops on REST servers and inactive gRPC replicas.
			if s.registry == nil || !s.registry.isConnected(targetNodeID) {
				continue
			}

			if scope == "containers" {
				switch msg.Payload {
				case "start":
					log.Printf("[gRPC] received container stream start for node %s via Redis", targetNodeID)
					if err := s.containerBroker.startStream(targetNodeID); err != nil {
						log.Printf("[gRPC] start container stream for node %s error: %v", targetNodeID, err)
					}
				case "stop":
					log.Printf("[gRPC] received container stream stop for node %s via Redis", targetNodeID)
					if err := s.containerBroker.stopStream(targetNodeID); err != nil {
						log.Printf("[gRPC] stop container stream for node %s error: %v", targetNodeID, err)
					}
				}
			} else if scope == "logs" {
				var req struct {
					Action string `json:"action"` // "start" or "stop"
					Unit   string `json:"unit"`
					Lines  int32  `json:"lines"`
				}
				if err := json.Unmarshal([]byte(msg.Payload), &req); err != nil {
					req.Action = msg.Payload
					req.Lines = 100
				}
				switch req.Action {
				case "start":
					log.Printf("[gRPC] received log stream start for node %s (unit=%q lines=%d) via Redis", targetNodeID, req.Unit, req.Lines)
					if err := s.logs.startStream(targetNodeID, req.Unit, req.Lines); err != nil {
						log.Printf("[gRPC] start log stream for node %s error: %v", targetNodeID, err)
					}
				case "stop":
					log.Printf("[gRPC] received log stream stop for node %s via Redis", targetNodeID)
					if s.redis != nil {
						channel := fmt.Sprintf("nodes:%s:logs", targetNodeID)
						if numMap, err := s.redis.PubSubNumSub(context.Background(), channel).Result(); err == nil {
							if numMap[channel] > 0 {
								log.Printf("[gRPC] skipping stop log stream for node %s: %d active subscribers still listening", targetNodeID, numMap[channel])
								continue
							}
						}
					}
					if err := s.logs.stopStream(targetNodeID); err != nil {
						log.Printf("[gRPC] stop log stream for node %s error: %v", targetNodeID, err)
					}
				}
			} else if scope == "jobs" {
				var req struct {
					JobID       string `json:"job_id"`
					CommandName string `json:"command_name"`
					CommandType string `json:"command_type"`
					Params      string `json:"params"`
				}
				if err := json.Unmarshal([]byte(msg.Payload), &req); err == nil {
					log.Printf("[gRPC] received remote job dispatch for node %s (%s) via Redis -> delivering to node", targetNodeID, req.CommandName)
					ct := pb.CommandType_COMMAND_TYPE_DEFAULT
					switch jobs.CommandType(req.CommandType) {
					case jobs.CommandTypeCustom:
						ct = pb.CommandType_COMMAND_TYPE_CUSTOM
					case jobs.CommandTypeInstallApp:
						ct = pb.CommandType_COMMAND_TYPE_INSTALL_APP
					case jobs.CommandTypeSwarm:
						ct = pb.CommandType_COMMAND_TYPE_SWARM
					}
					serverMsg := &pb.ServerMessage{
						Payload: &pb.ServerMessage_ExecuteJob{
							ExecuteJob: &pb.ExecuteJobPayload{
								JobId:       req.JobID,
								CommandName: req.CommandName,
								CommandType: ct,
								Params:      req.Params,
							},
						},
					}
					if err := s.SendToNode(targetNodeID, serverMsg); err != nil {
						log.Printf("[gRPC] send job %s to node %s error: %v", req.JobID, targetNodeID, err)
					}
				}
			} else if scope == "terminal" {
				var req struct {
					Action    string `json:"action"` // "start" | "input" | "resize" | "close"
					SessionID string `json:"session_id"`
					Cols      uint32 `json:"cols"`
					Rows      uint32 `json:"rows"`
					Shell     string `json:"shell"`
					Cwd       string `json:"cwd"`
					Data      []byte `json:"data"`
				}
				if err := json.Unmarshal([]byte(msg.Payload), &req); err == nil {
					switch req.Action {
					case "start":
						_, _ = s.StartTerminalSession(targetNodeID, req.SessionID, req.Cols, req.Rows, req.Shell, req.Cwd)
					case "input":
						_ = s.SendTerminalInput(targetNodeID, req.SessionID, req.Data)
					case "resize":
						_ = s.ResizeTerminalSession(targetNodeID, req.SessionID, req.Cols, req.Rows)
					case "close":
						s.CloseTerminalSession(targetNodeID, req.SessionID)
					}
				}
			}
		}
	}
}
