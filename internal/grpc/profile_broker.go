package grpcserver

// profile_broker.go — single-use channels that connect an incoming
// NodeMessage_ProfileData (arriving on the Connect gRPC stream) to the HTTP
// handler that is blocking and waiting for the result.

import (
	"sync"

	pb "github.com/arturo/autohost-cloud-api/internal/grpc/nodepb"
)

// profileBroker routes a ProfileDataPayload to the HTTP handler that requested
// it, identified by the correlation request_id field.
type profileBroker struct {
	mu      sync.Mutex
	pending map[string]chan *pb.ProfileDataPayload // requestID → one-shot channel
}

func newProfileBroker() *profileBroker {
	return &profileBroker{pending: make(map[string]chan *pb.ProfileDataPayload)}
}

// register creates and stores a buffered channel for the given requestID.
// The caller must call cancel(requestID) in a defer to clean up if the result
// is never delivered (timeout, node disconnect, etc.).
func (b *profileBroker) register(requestID string) <-chan *pb.ProfileDataPayload {
	ch := make(chan *pb.ProfileDataPayload, 1)
	b.mu.Lock()
	b.pending[requestID] = ch
	b.mu.Unlock()
	return ch
}

// deliver routes the payload to the waiting channel and removes the entry.
// It is safe to call from any goroutine.
func (b *profileBroker) deliver(payload *pb.ProfileDataPayload) {
	b.mu.Lock()
	ch, ok := b.pending[payload.GetRequestId()]
	if ok {
		delete(b.pending, payload.GetRequestId())
	}
	b.mu.Unlock()

	if ok {
		select {
		case ch <- payload:
		default:
			// Channel already has a value; nothing to do.
		}
	}
}

// cancel removes the pending entry (no-op if already delivered or never added).
func (b *profileBroker) cancel(requestID string) {
	b.mu.Lock()
	delete(b.pending, requestID)
	b.mu.Unlock()
}
