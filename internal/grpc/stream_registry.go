package grpcserver

import (
	"sync"

	pb "github.com/arturo/autohost-cloud-api/internal/grpc/nodepb"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// nodeStream holds the send side of an active gRPC Connect stream.
type nodeStream struct {
	send chan *pb.ServerMessage
}

// streamRegistry tracks which nodes have a live bidirectional gRPC stream.
type streamRegistry struct {
	mu      sync.RWMutex
	streams map[string]*nodeStream // nodeID -> active stream
}

func newStreamRegistry() *streamRegistry {
	return &streamRegistry{streams: make(map[string]*nodeStream)}
}

func (r *streamRegistry) register(nodeID string, ns *nodeStream) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.streams[nodeID] = ns
}

func (r *streamRegistry) unregister(nodeID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.streams, nodeID)
}

func (r *streamRegistry) get(nodeID string) (*nodeStream, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ns, ok := r.streams[nodeID]
	return ns, ok
}

func (r *streamRegistry) isConnected(nodeID string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.streams[nodeID]
	return ok
}

// sendToNode pushes a ServerMessage to a connected node.
func (r *streamRegistry) sendToNode(nodeID string, msg *pb.ServerMessage) error {
	ns, ok := r.get(nodeID)
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
