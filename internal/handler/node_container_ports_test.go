// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package handler

import (
	"encoding/json"
	"strings"
	"testing"

	pb "github.com/arturo/autohost-cloud-api/internal/grpc/nodepb"
)

func TestContainerListFromPayload_Ports(t *testing.T) {
	list := containerListFromPayload(&pb.ContainerListPayload{Containers: []*pb.ContainerInfo{
		{Id: "a", Name: "web", Ports: []*pb.ContainerPort{{PublicPort: 8080, PrivatePort: 80, Protocol: "tcp"}}},
		{Id: "b", Name: "worker"},
	}})

	data, err := json.Marshal(list)
	if err != nil {
		t.Fatal(err)
	}
	got := string(data)
	if !strings.Contains(got, `"ports":[{"public":8080,"private":80,"protocol":"tcp"}]`) {
		t.Errorf("ports not serialized as expected: %s", got)
	}
	if strings.Count(got, `"ports"`) != 1 {
		t.Errorf("a container without ports must omit the field: %s", got)
	}
}
