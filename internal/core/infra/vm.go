// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package infra

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/arturo/autohost-cloud-api/internal/core/jobs"
	"github.com/arturo/autohost-cloud-api/internal/domain/enrollment"
	"github.com/arturo/autohost-cloud-api/internal/domain/node"
	"github.com/arturo/autohost-cloud-api/internal/platform"
)

var (
	ErrVMNotFound      = errors.New("vm not found")
	ErrVMAlreadyExists = errors.New("vm with this name already exists on host")
	ErrVMInvalidSpecs  = errors.New("invalid vm specifications")
	ErrVMUnauthorized  = errors.New("unauthorized")

	// Backward compatibility aliases
	ErrNotFound      = ErrVMNotFound
	ErrAlreadyExists = ErrVMAlreadyExists
	ErrInvalidSpecs  = ErrVMInvalidSpecs
	ErrUnauthorized  = ErrVMUnauthorized
)

var validVMName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)

type VMStatus string

const (
	StatusCreating  VMStatus = "creating"
	StatusRunning   VMStatus = "running"
	StatusStopped   VMStatus = "stopped"
	StatusError     VMStatus = "error"
	StatusDestroyed VMStatus = "destroyed"
)

type VM struct {
	ID            string    `db:"id"               json:"id"`
	HostNodeID    string    `db:"host_node_id"     json:"host_node_id"`
	NodeID        *string   `db:"node_id"          json:"node_id,omitempty"`
	OwnerID       string    `db:"owner_id"         json:"owner_id"`
	Name          string    `db:"name"             json:"name"`
	VCPUs         int       `db:"vcpus"            json:"vcpus"`
	RAMMB         int       `db:"ram_mb"           json:"ram_mb"`
	DiskGB        int       `db:"disk_gb"          json:"disk_gb"`
	Image         string    `db:"image"            json:"image"`
	Status        string    `db:"status"           json:"status"`
	IPAddress     string    `db:"ip_address"       json:"ip_address"`
	CloudInitUser string    `db:"cloud_init_user"  json:"cloud_init_user"`
	ErrorMessage  string    `db:"error_message"    json:"error_message,omitempty"`
	CreatedAt     time.Time `db:"created_at"       json:"created_at"`
	UpdatedAt     time.Time `db:"updated_at"       json:"updated_at"`
}

type VMRepository interface {
	Create(ctx context.Context, v *VM) (*VM, error)
	GetByID(ctx context.Context, id string) (*VM, error)
	GetByHostAndName(ctx context.Context, hostNodeID, name string) (*VM, error)
	ListByHost(ctx context.Context, hostNodeID string) ([]*VM, error)
	ListByOwner(ctx context.Context, ownerID string) ([]*VM, error)
	Update(ctx context.Context, v *VM) (*VM, error)
	Delete(ctx context.Context, id string) error
	LinkNodeByHostname(ctx context.Context, ownerID, hostname, nodeID, ipLocal string) error
}

type VMDispatcher interface {
	DispatchJobWithParams(nodeID, jobID, commandName string, commandType jobs.CommandType, params string) error
	IsNodeConnected(nodeID string) bool
}

type CreateVMRequest struct {
	Name          string   `json:"name"`
	VCPUs         int      `json:"vcpus"`
	RAMMB         int      `json:"ram_mb"`
	DiskGB        int      `json:"disk_gb"`
	Image         string   `json:"image"`
	CloudInitUser string   `json:"cloud_init_user"`
	SSHKeys       []string `json:"ssh_keys"`
}

type VMService struct {
	repo       VMRepository
	nodeRepo   node.Repository
	enrollSvc  *enrollment.Service
	jobService *jobs.JobService
	dispatcher VMDispatcher
}

func NewVMService(
	repo VMRepository,
	nodeRepo node.Repository,
	enrollSvc *enrollment.Service,
	jobService *jobs.JobService,
	dispatcher VMDispatcher,
) *VMService {
	return &VMService{
		repo:       repo,
		nodeRepo:   nodeRepo,
		enrollSvc:  enrollSvc,
		jobService: jobService,
		dispatcher: dispatcher,
	}
}

// CreateVM validates specs, generates an enrollment token, persists the VM
// record, and dispatches the creation job to the host agent.
func (s *VMService) CreateVM(ctx context.Context, hostNodeID, userID string, req CreateVMRequest) (*VM, error) {
	name := strings.TrimSpace(req.Name)
	if !validVMName.MatchString(name) {
		return nil, fmt.Errorf("%w: name must be 1-64 chars [a-zA-Z0-9_-]", ErrVMInvalidSpecs)
	}

	// 1. Verify host node exists and belongs to the caller
	hostNode, err := s.nodeRepo.FindByID(hostNodeID)
	if err != nil || hostNode == nil {
		return nil, errors.New("host node not found")
	}
	if hostNode.OwnerID == nil || *hostNode.OwnerID != userID {
		return nil, ErrVMUnauthorized
	}

	// 2. Check name uniqueness on this host
	if existing, _ := s.repo.GetByHostAndName(ctx, hostNodeID, name); existing != nil {
		return nil, ErrVMAlreadyExists
	}

	// 3. Defaults & bounds
	vcpus := req.VCPUs
	if vcpus < 1 {
		vcpus = 2
	}
	if vcpus > 64 {
		vcpus = 64
	}

	ramMB := req.RAMMB
	if ramMB < 512 {
		ramMB = 2048
	}
	if ramMB > 262144 {
		ramMB = 262144
	}

	diskGB := req.DiskGB
	if diskGB < 5 {
		diskGB = 20
	}
	if diskGB > 2000 {
		diskGB = 2000
	}

	image := strings.ToLower(strings.TrimSpace(req.Image))
	if image == "" {
		image = "ubuntu-24.04"
	}

	user := strings.TrimSpace(req.CloudInitUser)
	if user == "" {
		if strings.Contains(image, "debian") {
			user = "debian"
		} else {
			user = "ubuntu"
		}
	}

	// 4. Generate child enrollment token for auto-enrollment
	plainToken, hash, err := platform.GenerateEnrollToken()
	if err != nil {
		return nil, fmt.Errorf("generate enroll token: %w", err)
	}

	expiresAt := time.Now().Add(24 * time.Hour)
	if err := s.enrollSvc.CreateEnrollToken(hash, userID, expiresAt); err != nil {
		return nil, fmt.Errorf("save enroll token: %w", err)
	}

	// 5. Persist VM record
	v := &VM{
		HostNodeID:    hostNodeID,
		OwnerID:       userID,
		Name:          name,
		VCPUs:         vcpus,
		RAMMB:         ramMB,
		DiskGB:        diskGB,
		Image:         image,
		Status:        string(StatusCreating),
		CloudInitUser: user,
	}

	created, err := s.repo.Create(ctx, v)
	if err != nil {
		return nil, fmt.Errorf("create vm record: %w", err)
	}

	// 6. Create job and dispatch vm.create to host node
	apiURL := os.Getenv("API_PUBLIC_URL")
	if apiURL == "" {
		apiURL = os.Getenv("API_URL")
	}
	if apiURL == "" || strings.Contains(apiURL, "localhost") || strings.Contains(apiURL, "127.0.0.1") {
		// Attempt to resolve outbound host IP so remote hypervisors & guest VMs can reach this API
		if conn, err := net.Dial("udp", "8.8.8.8:80"); err == nil {
			localAddr := conn.LocalAddr().(*net.UDPAddr)
			conn.Close()
			port := os.Getenv("PORT")
			if port == "" {
				port = "8080"
			}
			apiURL = fmt.Sprintf("http://%s:%s", localAddr.IP.String(), port)
		}
	}
	if apiURL == "" {
		apiURL = "http://localhost:8080"
	}

	params := map[string]any{
		"vm_id":         created.ID,
		"name":          created.Name,
		"vcpus":         created.VCPUs,
		"ram_mb":        created.RAMMB,
		"disk_gb":       created.DiskGB,
		"image":         created.Image,
		"user":          created.CloudInitUser,
		"ssh_keys":      req.SSHKeys,
		"enroll_token":  plainToken,
		"cloud_api_url": apiURL,
	}
	paramsJSON, _ := json.Marshal(params)

	if s.jobService != nil {
		j, err := s.jobService.DispatchWithParams(hostNodeID, "vm.create", jobs.CommandTypeDefault, string(paramsJSON))
		if err == nil && j != nil {
			if s.dispatcher != nil {
				if err := s.dispatcher.DispatchJobWithParams(hostNodeID, j.ID, "vm.create", jobs.CommandTypeDefault, string(paramsJSON)); err != nil {
					log.Printf("[VM Service] dispatch vm.create error: %v", err)
				}
			}
		}
	}

	return created, nil
}

// ActionVM runs a lifecycle action (start, stop, reboot, destroy) against a VM.
func (s *VMService) ActionVM(ctx context.Context, vmID, userID, action string) error {
	v, err := s.repo.GetByID(ctx, vmID)
	if err != nil {
		return err
	}
	if v.OwnerID != userID {
		return ErrVMUnauthorized
	}

	action = strings.ToLower(strings.TrimSpace(action))
	switch action {
	case "start", "stop", "reboot", "destroy":
	default:
		return fmt.Errorf("unsupported action %q", action)
	}

	commandName := fmt.Sprintf("vm.%s.%s", action, v.Name)

	if s.jobService != nil {
		j, err := s.jobService.DispatchWithParams(v.HostNodeID, commandName, jobs.CommandTypeDefault, "")
		if err == nil && j != nil && s.dispatcher != nil {
			_ = s.dispatcher.DispatchJobWithParams(v.HostNodeID, j.ID, commandName, jobs.CommandTypeDefault, "")
		}
	}

	if action == "destroy" {
		return s.repo.Delete(ctx, vmID)
	}

	if action == "start" {
		v.Status = string(StatusRunning)
		_, _ = s.repo.Update(ctx, v)
	} else if action == "stop" {
		v.Status = string(StatusStopped)
		_, _ = s.repo.Update(ctx, v)
	}

	return nil
}

// ListByHost returns all VMs created on the given host node.
func (s *VMService) ListByHost(ctx context.Context, hostNodeID, userID string) ([]*VM, error) {
	hostNode, err := s.nodeRepo.FindByID(hostNodeID)
	if err != nil || hostNode == nil {
		return nil, errors.New("host node not found")
	}
	if hostNode.OwnerID == nil || *hostNode.OwnerID != userID {
		return nil, ErrVMUnauthorized
	}
	return s.repo.ListByHost(ctx, hostNodeID)
}

// GetByID returns the details of a specific VM.
func (s *VMService) GetByID(ctx context.Context, vmID, userID string) (*VM, error) {
	v, err := s.repo.GetByID(ctx, vmID)
	if err != nil {
		return nil, err
	}
	if v.OwnerID != userID {
		return nil, ErrVMUnauthorized
	}
	return v, nil
}
