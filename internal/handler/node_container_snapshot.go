// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/arturo/autohost-cloud-api/internal/core/identity"
	grpcserver "github.com/arturo/autohost-cloud-api/internal/grpc"
	pb "github.com/arturo/autohost-cloud-api/internal/grpc/nodepb"
	"github.com/arturo/autohost-cloud-api/internal/handler/middleware"
	"github.com/arturo/autohost-cloud-api/internal/handler/response"
)

// defaultContainerSnapshotTimeout bounds how long listContainers waits for the agent to report. It stays
// well under the server's 30 s WriteTimeout and the timeouts of HTTP clients such as MCP proxies.
const defaultContainerSnapshotTimeout = 10 * time.Second

// containerSnapshotSource gets one container list from a node. Fetch asks the agent for a fresh snapshot
// and waits until ctx is done; Cached returns the last snapshot seen recently, if any. Both return the
// JSON encoding of sseContainerList.
type containerSnapshotSource interface {
	Online(ctx context.Context, nodeID string) bool
	Cached(ctx context.Context, nodeID string) ([]byte, bool)
	Fetch(ctx context.Context, nodeID string) ([]byte, error)
}

// containerListResponse is the body of GET /v1/node-containers/{nodeId}.
type containerListResponse struct {
	sseContainerList
	// Cached is true when the agent did not answer and the list is the last snapshot seen (up to 10 min old).
	Cached bool `json:"cached"`
}

func (h *NodeContainerHandler) snapshotSource() containerSnapshotSource {
	switch {
	case h.snapshots != nil:
		return h.snapshots
	case h.redis != nil:
		return redisContainerSource{rdb: h.redis, grpc: h.grpc}
	case h.grpc != nil:
		return brokerContainerSource{grpc: h.grpc}
	default:
		return nil
	}
}

// listContainers returns the node's containers once, for clients that cannot consume the SSE stream
// (scripts, MCP clients). It asks the agent for a fresh snapshot and falls back to the cached one.
func (h *NodeContainerHandler) listContainers(w http.ResponseWriter, r *http.Request) {
	userID, ok := middleware.RequireUserID(w, r)
	if !ok {
		return
	}
	nodeID := nodeIDParam(r)
	if h.rbacSvc == nil {
		response.RespondError(w, http.StatusForbidden, "forbidden")
		return
	}
	if allowed, err := h.rbacSvc.HasNodePermission(r.Context(), userID, nodeID, identity.PermContainersRead); err != nil || !allowed {
		response.RespondError(w, http.StatusForbidden, "forbidden")
		return
	}

	src := h.snapshotSource()
	if src == nil {
		response.RespondError(w, http.StatusServiceUnavailable, "container listing is not available on this server")
		return
	}

	cached, hasCached := src.Cached(r.Context(), nodeID)
	if !src.Online(r.Context(), nodeID) {
		if hasCached {
			writeContainerList(w, cached, true)
			return
		}
		response.RespondError(w, http.StatusConflict, "node is not connected")
		return
	}

	timeout := h.snapshotTimeout
	if timeout <= 0 {
		timeout = defaultContainerSnapshotTimeout
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()

	data, err := src.Fetch(ctx, nodeID)
	switch {
	case err == nil:
		writeContainerList(w, data, false)
	case hasCached:
		writeContainerList(w, cached, true)
	case errors.Is(err, context.DeadlineExceeded):
		response.RespondError(w, http.StatusGatewayTimeout, "node did not report its containers in time")
	default:
		log.Printf("[ERROR] list containers for node %s: %v", nodeID, err)
		response.RespondError(w, http.StatusInternalServerError, "could not list containers")
	}
}

func writeContainerList(w http.ResponseWriter, data []byte, cached bool) {
	var list sseContainerList
	if err := json.Unmarshal(data, &list); err != nil {
		log.Printf("[ERROR] decode container snapshot: %v", err)
		response.RespondError(w, http.StatusInternalServerError, "could not list containers")
		return
	}
	if list.Containers == nil {
		list.Containers = []sseContainerInfo{}
	}
	response.RespondJSON(w, http.StatusOK, containerListResponse{sseContainerList: list, Cached: cached})
}

// containerListFromPayload converts an agent snapshot to the JSON shape served to clients.
func containerListFromPayload(snapshot *pb.ContainerListPayload) sseContainerList {
	list := sseContainerList{
		Error:      snapshot.GetError(),
		Containers: make([]sseContainerInfo, 0, len(snapshot.GetContainers())),
	}
	for _, c := range snapshot.GetContainers() {
		list.Containers = append(list.Containers, sseContainerInfo{
			ID:      c.GetId(),
			Name:    c.GetName(),
			Image:   c.GetImage(),
			State:   c.GetState(),
			Status:  c.GetStatus(),
			CPU:     c.GetCpu(),
			Mem:     c.GetMem(),
			MemPerc: c.GetMemPerc(),
			Ports:   containerPortsFromPB(c.GetPorts()),
		})
	}
	return list
}

func containerPortsFromPB(ports []*pb.ContainerPort) []sseContainerPort {
	if len(ports) == 0 {
		return nil
	}
	out := make([]sseContainerPort, 0, len(ports))
	for _, p := range ports {
		out = append(out, sseContainerPort{Public: p.GetPublicPort(), Private: p.GetPrivatePort(), Protocol: p.GetProtocol()})
	}
	return out
}

// redisContainerSource talks to the agent through the gRPC instance that holds its stream, using the
// same Redis channels as the SSE stream (see NodeAgentServer.listenContainerCommands).
type redisContainerSource struct {
	rdb  *redis.Client
	grpc *grpcserver.NodeAgentServer
}

func (s redisContainerSource) Online(ctx context.Context, nodeID string) bool {
	if n, err := s.rdb.Exists(ctx, fmt.Sprintf("nodes:%s:online", nodeID)).Result(); err == nil && n > 0 {
		return true
	}
	return s.grpc != nil && s.grpc.IsNodeConnected(nodeID)
}

func (s redisContainerSource) Cached(ctx context.Context, nodeID string) ([]byte, bool) {
	v, err := s.rdb.Get(ctx, fmt.Sprintf("nodes:%s:containers:latest", nodeID)).Bytes()
	return v, err == nil && len(v) > 0
}

func (s redisContainerSource) Fetch(ctx context.Context, nodeID string) ([]byte, error) {
	channel := fmt.Sprintf("nodes:%s:containers", nodeID)
	pubsub := s.rdb.Subscribe(ctx, channel)
	// Wait for the subscription to be active so the snapshot triggered below cannot be missed.
	if _, err := pubsub.Receive(ctx); err != nil {
		_ = pubsub.Close()
		return nil, err
	}
	defer func() {
		_ = pubsub.Close()
		// Stop the agent's stream only when nobody else (such as an open browser tab) is listening.
		bg, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if subs, err := s.rdb.PubSubNumSub(bg, channel).Result(); err == nil && subs[channel] == 0 {
			_ = s.rdb.Publish(bg, channel+":cmd", "stop").Err()
		}
	}()

	if err := s.rdb.Publish(ctx, channel+":cmd", "start").Err(); err != nil {
		return nil, err
	}
	select {
	case msg, ok := <-pubsub.Channel():
		if !ok {
			return nil, errors.New("container subscription closed")
		}
		return []byte(msg.Payload), nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// brokerContainerSource uses the in-process broker when the API runs without Redis (SERVICE_MODE=all).
type brokerContainerSource struct {
	grpc *grpcserver.NodeAgentServer
}

func (s brokerContainerSource) Online(_ context.Context, nodeID string) bool {
	return s.grpc.IsNodeConnected(nodeID)
}

func (s brokerContainerSource) Cached(context.Context, string) ([]byte, bool) { return nil, false }

func (s brokerContainerSource) Fetch(ctx context.Context, nodeID string) ([]byte, error) {
	ch, cancel := s.grpc.SubscribeToContainers(nodeID)
	defer cancel()
	select {
	case snapshot, ok := <-ch:
		if !ok {
			return nil, errors.New("container subscription closed")
		}
		return json.Marshal(containerListFromPayload(snapshot))
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
