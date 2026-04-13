package grpcserver

import (
	"context"
	"io"
	"log"

	"github.com/arturo/autohost-cloud-api/internal/domain/job"
	"github.com/arturo/autohost-cloud-api/internal/domain/marketplace"
	"github.com/arturo/autohost-cloud-api/internal/domain/node"
	nodealert "github.com/arturo/autohost-cloud-api/internal/domain/node_alert"
	nodecommand "github.com/arturo/autohost-cloud-api/internal/domain/node_command"
	nodetoken "github.com/arturo/autohost-cloud-api/internal/domain/node_token"
	pb "github.com/arturo/autohost-cloud-api/internal/grpc/nodepb"
)

// NodeAgentServer implements pb.NodeAgentServiceServer.
type NodeAgentServer struct {
	pb.UnimplementedNodeAgentServiceServer

	commandSvc      *nodecommand.Service
	jobSvc          *job.Service
	tokenSvc        *nodetoken.Service
	alertSvc        *nodealert.Service
	nodeSvc         *node.Service
	marketplaceRepo marketplace.Repository

	registry *streamRegistry
	logs     *logBroker
}

// NewNodeAgentServer creates a ready-to-register gRPC server.
func NewNodeAgentServer(
	commandSvc *nodecommand.Service,
	jobSvc *job.Service,
	tokenSvc *nodetoken.Service,
	alertSvc *nodealert.Service,
	nodeSvc *node.Service,
	marketplaceRepo marketplace.Repository,
) *NodeAgentServer {
	reg := newStreamRegistry()
	s := &NodeAgentServer{
		commandSvc:      commandSvc,
		jobSvc:          jobSvc,
		tokenSvc:        tokenSvc,
		alertSvc:        alertSvc,
		nodeSvc:         nodeSvc,
		marketplaceRepo: marketplaceRepo,
		registry:        reg,
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
			log.Printf("[gRPC] heartbeat from node %s", p.Heartbeat.GetNodeId())

		case *pb.NodeMessage_LogEntry:
			s.logs.fanOut(nodeID, p.LogEntry)

		default:
			log.Printf("[gRPC] unknown payload from node %s", nodeID)
		}
	}

	return nil
}

// ---- ReportMetrics ----------------------------------------------------------

func (s *NodeAgentServer) ReportMetrics(
	ctx context.Context,
	req *pb.MetricRequest,
) (*pb.MetricResponse, error) {
	nodeID, err := s.nodeIDFromCtx(ctx)
	if err != nil {
		return nil, err
	}

	log.Printf("[gRPC] metrics from node %s: CPU %.2f%%, RAM %.2f%%", nodeID, req.GetCpuUsagePercent(), req.GetRamUsagePercent())

	// Aquí llamarías a tu servicio de dominio para persistir los datos
	// return s.nodeSvc.SaveMetrics(nodeID, req)

	return &pb.MetricResponse{Success: true}, nil
}
