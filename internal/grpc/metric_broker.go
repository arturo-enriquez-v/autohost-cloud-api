package grpcserver

import (
	"sync"
	"sync/atomic"

	pb "github.com/arturo/autohost-cloud-api/internal/grpc/nodepb"
)

// metricSubscriber receives fan-out metric payloads for a node.
type metricSubscriber struct {
	ch chan *pb.MetricPayload
}

// metricBroker manages per-node pub/sub of live metrics between the gRPC
// Connect stream and zero-or-more SSE subscribers.
type metricBroker struct {
	mu        sync.RWMutex
	subs      map[string]map[uint64]*metricSubscriber // nodeID -> subID -> subscriber
	nextSubID uint64
}

func newMetricBroker() *metricBroker {
	return &metricBroker{
		subs: make(map[string]map[uint64]*metricSubscriber),
	}
}

// subscribe returns a channel that receives metric payloads from the given node
// and a cancel function the caller must invoke when done.
func (b *metricBroker) subscribe(nodeID string) (<-chan *pb.MetricPayload, func()) {
	id := atomic.AddUint64(&b.nextSubID, 1)
	ch := make(chan *pb.MetricPayload, 16)

	b.mu.Lock()
	if b.subs[nodeID] == nil {
		b.subs[nodeID] = make(map[uint64]*metricSubscriber)
	}
	b.subs[nodeID][id] = &metricSubscriber{ch: ch}
	b.mu.Unlock()

	cancel := func() {
		b.mu.Lock()
		delete(b.subs[nodeID], id)
		if len(b.subs[nodeID]) == 0 {
			delete(b.subs, nodeID)
		}
		b.mu.Unlock()
		close(ch)
	}
	return ch, cancel
}

// fanOut delivers a metric payload to every subscriber of a node.
func (b *metricBroker) fanOut(nodeID string, m *pb.MetricPayload) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, sub := range b.subs[nodeID] {
		select {
		case sub.ch <- m:
		default:
			// subscriber too slow — drop the frame
		}
	}
}
