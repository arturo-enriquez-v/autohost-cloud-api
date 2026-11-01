// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package node

import (
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/arturo/autohost-cloud-api/internal/core/identity"
	"github.com/arturo/autohost-cloud-api/internal/platform"
)

// LocalNodeBootstrapper handles automatic registration and credential provisioning
// for the local host node in self-hosted All-in-One deployments.
type LocalNodeBootstrapper struct {
	nodeSvc  *NodeService
	tokenSvc *NodeTokenService
	authSvc  *identity.AuthService
	pki      *platform.PKIManager
}

// NewLocalNodeBootstrapper constructs the bootstrapper, wires the first-user hook,
// and triggers initial local node provisioning when AUTO_ENROLL_LOCAL_NODE is enabled.
func NewLocalNodeBootstrapper(
	nodeSvc *NodeService,
	tokenSvc *NodeTokenService,
	authSvc *identity.AuthService,
	pki *platform.PKIManager,
) *LocalNodeBootstrapper {
	b := &LocalNodeBootstrapper{
		nodeSvc:  nodeSvc,
		tokenSvc: tokenSvc,
		authSvc:  authSvc,
		pki:      pki,
	}

	if authSvc != nil {
		authSvc.SetOnFirstUserCreated(func(userID string) {
			if err := b.ClaimAndEnsureLocalNode(userID); err != nil {
				log.Printf("[WARN] local-node: failed to claim/ensure local node for initial admin %s: %v", userID, err)
			}
		})
	}

	if isAutoEnrollEnabled() {
		if err := b.Bootstrap(); err != nil {
			log.Printf("[WARN] local-node: auto-enrollment skipped or failed: %v", err)
		}
	}

	return b
}

func isAutoEnrollEnabled() bool {
	val := strings.ToLower(strings.TrimSpace(os.Getenv("AUTO_ENROLL_LOCAL_NODE")))
	// Enabled by default in self-hosted mode unless explicitly disabled with "false" or "0".
	return val != "false" && val != "0" && val != "no"
}

// ClaimAndEnsureLocalNode assigns any unowned nodes to the newly created admin user
// and ensures the local master node is provisioned.
func (b *LocalNodeBootstrapper) ClaimAndEnsureLocalNode(userID string) error {
	if err := b.nodeSvc.ClaimUnownedNodes(userID); err != nil {
		return fmt.Errorf("claim unowned nodes: %w", err)
	}
	if isAutoEnrollEnabled() {
		return b.Bootstrap()
	}
	return nil
}

// Bootstrap ensures that the local master node exists in the database and that
// its mTLS certificates and agent.yaml are written to disk for the local agent.
func (b *LocalNodeBootstrapper) Bootstrap() error {
	var ownerID *string
	if b.authSvc != nil {
		if firstUser, err := b.authSvc.GetFirstUser(); err == nil && firstUser != nil {
			ownerID = &firstUser.ID
			_ = b.nodeSvc.ClaimUnownedNodes(firstUser.ID)
		}
	}

	localNode, err := b.nodeSvc.FindLocalNode()
	if err != nil {
		return fmt.Errorf("find local node: %w", err)
	}

	isNewNode := false
	if localNode == nil {
		hostname := strings.TrimSpace(os.Getenv("LOCAL_NODE_HOSTNAME"))
		if hostname == "" {
			if h, err := os.Hostname(); err == nil && h != "" {
				hostname = h
			} else {
				hostname = "autohost-local"
			}
		}

		ipLocal := detectPrimaryLocalIP()
		created, err := b.nodeSvc.Register(&Node{
			Hostname:     hostname,
			IPLocal:      ipLocal,
			OS:           runtime.GOOS,
			Arch:         runtime.GOARCH,
			VersionAgent: "selfhosted-local",
			OwnerID:      ownerID,
			IsLocal:      true,
		})
		if err != nil {
			return fmt.Errorf("register local node: %w", err)
		}
		localNode = created
		isNewNode = true
		log.Printf("[INFO] local-node: registered Master / Local Node %s (%s - %s)", localNode.Hostname, localNode.ID, localNode.IPLocal)
	}

	configDir := resolveWritableAgentDir()
	agentYAMLPath := filepath.Join(configDir, "agent.yaml")

	// Check if agent.yaml already exists and references the current localNode.ID
	if !isNewNode {
		if data, err := os.ReadFile(agentYAMLPath); err == nil && strings.Contains(string(data), localNode.ID) {
			return nil
		}
	}

	plainToken, hashToken, err := platform.GenerateTokenApi()
	if err != nil {
		return fmt.Errorf("generate local node token: %w", err)
	}
	if err := b.tokenSvc.CreateNodeToken(localNode.ID, hashToken); err != nil {
		return fmt.Errorf("save local node token: %w", err)
	}

	certsDir := filepath.Join(configDir, "certs")
	if err := os.MkdirAll(certsDir, 0750); err != nil {
		return fmt.Errorf("create local agent certs dir %s: %w", certsDir, err)
	}

	caCertPath := filepath.Join(certsDir, "ca.crt")
	clientCertPath := filepath.Join(certsDir, "client.crt")
	clientKeyPath := filepath.Join(certsDir, "client.key")

	if b.pki != nil {
		clientCert, clientKey, err := b.pki.IssueClientCert(localNode.ID)
		if err != nil {
			return fmt.Errorf("issue local node mTLS cert: %w", err)
		}
		if err := os.WriteFile(caCertPath, []byte(b.pki.CACertPEM()), 0644); err != nil {
			return fmt.Errorf("write ca.crt: %w", err)
		}
		if err := os.WriteFile(clientCertPath, clientCert, 0644); err != nil {
			return fmt.Errorf("write client.crt: %w", err)
		}
		if err := os.WriteFile(clientKeyPath, clientKey, 0640); err != nil {
			return fmt.Errorf("write client.key: %w", err)
		}
	}

	grpcAddr := os.Getenv("LOCAL_AGENT_GRPC_ADDRESS")
	if grpcAddr == "" {
		port := os.Getenv("GRPC_PORT")
		if port == "" {
			port = "9090"
		}
		grpcAddr = "127.0.0.1:" + port
	}

	yamlContent := fmt.Sprintf(`# Auto-generated by AutoHost All-in-One Local Node Bootstrapper
node_id: "%s"
api_token: "%s"
grpc_address: "%s"
grpc_ca_cert: "%s"
grpc_client_cert: "%s"
grpc_client_key: "%s"
`, localNode.ID, plainToken, grpcAddr, caCertPath, clientCertPath, clientKeyPath)

	if err := os.WriteFile(agentYAMLPath, []byte(yamlContent), 0640); err != nil {
		return fmt.Errorf("write local agent.yaml: %w", err)
	}

	log.Printf("[INFO] local-node: provisioned local agent credentials and mTLS certs in %s", configDir)
	return nil
}

func resolveWritableAgentDir() string {
	preferred := platform.LocalAgentConfigDir()
	if err := os.MkdirAll(preferred, 0750); err == nil {
		testFile := filepath.Join(preferred, ".write_test")
		if err := os.WriteFile(testFile, []byte("ok"), 0600); err == nil {
			_ = os.Remove(testFile)
			return preferred
		}
	}
	fallback := filepath.Join(platform.PKICertsDir(), "local-agent")
	_ = os.MkdirAll(fallback, 0750)
	return fallback
}

func detectPrimaryLocalIP() string {
	addrs, err := net.InterfaceAddrs()
	if err == nil {
		for _, a := range addrs {
			if ipnet, ok := a.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
				if ip4 := ipnet.IP.To4(); ip4 != nil {
					return ip4.String()
				}
			}
		}
	}
	return "127.0.0.1"
}

