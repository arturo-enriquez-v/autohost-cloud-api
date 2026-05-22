package grpcserver

import (
	"log"
	"sync"
	"sync/atomic"

	pb "github.com/arturo/autohost-cloud-api/internal/grpc/nodepb"
)

// containerSubscriber receives fan-out container list snapshots for a node.
type containerSubscriber struct {
	ch chan *pb.ContainerListPayload
}

// containerBroker manages per-node pub/sub of Docker container list snapshots
// between the gRPC Connect stream and zero-or-more SSE subscribers.
//
// It follows the same lifecycle as logBroker: when the first subscriber
// registers it tells the agent to start streaming via StreamContainersPayload;
// when the last subscriber leaves it sends StopContainersPayload.
type containerBroker struct {
	mu        sync.RWMutex
	subs      map[string]map[uint64]*containerSubscriber // nodeID -> subID -> subscriber
	nextSubID uint64

	// sendCmd is injected by the server so the broker can ask the agent to
	// start/stop container streaming without a direct dependency on streamRegistry.
	sendCmd func(nodeID string, msg *pb.ServerMessage) error
}

func newContainerBroker(sendCmd func(string, *pb.ServerMessage) error) *containerBroker {
	return &containerBroker{
		subs:    make(map[string]map[uint64]*containerSubscriber),
		sendCmd: sendCmd,
	}
}

// hasSubscribers reports whether nodeID has at least one active subscriber.
// Used on agent reconnect to decide whether to re-issue StreamContainers.
func (b *containerBroker) hasSubscribers(nodeID string) bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return len(b.subs[nodeID]) > 0
}

// subscribe returns a channel that receives ContainerListPayload snapshots from
// the given node and a cancel function the caller must invoke when done.
// If this is the first subscriber, it also asks the agent to start streaming.
func (b *containerBroker) subscribe(nodeID string) (<-chan *pb.ContainerListPayload, func()) {
	id := atomic.AddUint64(&b.nextSubID, 1)
	ch := make(chan *pb.ContainerListPayload, 8)

	b.mu.Lock()
	if b.subs[nodeID] == nil {
		b.subs[nodeID] = make(map[uint64]*containerSubscriber)
	}
	first := len(b.subs[nodeID]) == 0
	b.subs[nodeID][id] = &containerSubscriber{ch: ch}
	b.mu.Unlock()

	if first {
		if err := b.startStream(nodeID); err != nil {
			log.Printf("[gRPC] start container stream for node %s: %v", nodeID, err)
		}
	}

	cancel := func() {
		b.mu.Lock()
		delete(b.subs[nodeID], id)
		last := len(b.subs[nodeID]) == 0
		if last {
			delete(b.subs, nodeID)
		}
		b.mu.Unlock()
		close(ch)

		if last {
			if err := b.stopStream(nodeID); err != nil {
				log.Printf("[gRPC] stop container stream for node %s: %v", nodeID, err)
			}
		}
	}
	return ch, cancel
}

// fanOut delivers a ContainerListPayload snapshot to every subscriber of a node.
func (b *containerBroker) fanOut(nodeID string, snapshot *pb.ContainerListPayload) {
	b.mu.RLock()
	subs := b.subs[nodeID]
	b.mu.RUnlock()
	for _, sub := range subs {
		select {
		case sub.ch <- snapshot:
		default: // subscriber too slow — drop the frame
		}
	}
}

// startStream sends a StreamContainersPayload command to the agent.
func (b *containerBroker) startStream(nodeID string) error {
	return b.sendCmd(nodeID, &pb.ServerMessage{
		Payload: &pb.ServerMessage_StreamContainers{
			StreamContainers: &pb.StreamContainersPayload{},
		},
	})
}

// stopStream asks the agent to stop streaming container state.
func (b *containerBroker) stopStream(nodeID string) error {
	return b.sendCmd(nodeID, &pb.ServerMessage{
		Payload: &pb.ServerMessage_StopContainers{
			StopContainers: &pb.StopContainersPayload{},
		},
	})
}
