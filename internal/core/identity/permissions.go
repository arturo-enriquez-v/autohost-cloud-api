// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package identity

import (
	"slices"
	"strings"
)

// Global platform roles
const (
	RoleAdmin  = "admin"
	RoleMember = "member"
)

// Node-level role presets
const (
	NodePresetAdmin    = "admin"
	NodePresetViewer   = "viewer"
	NodePresetOperator = "operator"
	NodePresetEditor   = "editor"
	NodePresetCustom   = "custom"
)

// Granular node permissions
const (
	PermWildcard = "*"

	// Node general
	PermNodeRead   = "node:read"
	PermNodeUpdate = "node:update"
	PermNodeDelete = "node:delete"

	// Metrics & Service Monitors
	PermMetricsRead    = "metrics:read"
	PermMonitorsRead   = "monitors:read"
	PermMonitorsManage = "monitors:manage"

	// Docker Containers & Marketplace
	PermContainersRead     = "containers:read"
	PermContainersAction   = "containers:action"
	PermContainersDeploy   = "containers:deploy"
	PermContainersSettings = "containers:settings"

	// Interactive Web Terminal (PTY)
	PermTerminalExec = "terminal:exec"

	// File Explorer & Public Shares
	PermFilesRead   = "files:read"
	PermFilesWrite  = "files:write"
	PermFilesDelete = "files:delete"
	PermFilesShare  = "files:share"

	// Commands, Jobs & Scheduled Tasks
	PermJobsRead   = "jobs:read"
	PermJobsRun    = "jobs:run"
	PermJobsManage = "jobs:manage"

	// Logs & AI Diagnostics
	PermLogsRead   = "logs:read"
	PermLogsManage = "logs:manage"

	// Network, Reverse Proxy (Caddy), Cloudflare Tunnel & Tailscale Funnel
	PermNetworkRead   = "network:read"
	PermNetworkManage = "network:manage"

	// Backups & Restore
	PermBackupsRead    = "backups:read"
	PermBackupsCreate  = "backups:create"
	PermBackupsRestore = "backups:restore"
	PermBackupsManage  = "backups:manage"

	// Virtual Machines (KVM / QEMU)
	PermVMsRead   = "vms:read"
	PermVMsAction = "vms:action"
	PermVMsManage = "vms:manage"

	// Alerts
	PermAlertsRead   = "alerts:read"
	PermAlertsManage = "alerts:manage"

	// Connectors (saved HTTP requests to external APIs). They belong to a project, so these permissions
	// are taken from the user's project membership, not from per-node grants.
	PermConnectorsRead   = "connectors:read"
	PermConnectorsRun    = "connectors:run"
	PermConnectorsManage = "connectors:manage"
)

// AllNodePermissions lists every valid granular node permission in canonical order.
var AllNodePermissions = []string{
	PermNodeRead,
	PermNodeUpdate,
	PermNodeDelete,
	PermMetricsRead,
	PermMonitorsRead,
	PermMonitorsManage,
	PermContainersRead,
	PermContainersAction,
	PermContainersDeploy,
	PermContainersSettings,
	PermTerminalExec,
	PermFilesRead,
	PermFilesWrite,
	PermFilesDelete,
	PermFilesShare,
	PermJobsRead,
	PermJobsRun,
	PermJobsManage,
	PermLogsRead,
	PermLogsManage,
	PermNetworkRead,
	PermNetworkManage,
	PermBackupsRead,
	PermBackupsCreate,
	PermBackupsRestore,
	PermBackupsManage,
	PermVMsRead,
	PermVMsAction,
	PermVMsManage,
	PermAlertsRead,
	PermAlertsManage,
	PermConnectorsRead,
	PermConnectorsRun,
	PermConnectorsManage,
}

// PresetViewerPermissions defines read-only permissions across a node.
var PresetViewerPermissions = []string{
	PermNodeRead,
	PermMetricsRead,
	PermMonitorsRead,
	PermContainersRead,
	PermFilesRead,
	PermJobsRead,
	PermLogsRead,
	PermNetworkRead,
	PermBackupsRead,
	PermVMsRead,
	PermAlertsRead,
}

// PresetOperatorPermissions defines day-to-day operational permissions without destructive config changes.
var PresetOperatorPermissions = []string{
	PermNodeRead,
	PermMetricsRead,
	PermMonitorsRead,
	PermContainersRead,
	PermContainersAction,
	PermFilesRead,
	PermFilesWrite,
	PermJobsRead,
	PermJobsRun,
	PermLogsRead,
	PermLogsManage,
	PermNetworkRead,
	PermBackupsRead,
	PermBackupsCreate,
	PermVMsRead,
	PermVMsAction,
	PermAlertsRead,
}

// PresetEditorPermissions defines full node management permissions (excluding deleting the node from the platform).
var PresetEditorPermissions = []string{
	PermNodeRead,
	PermNodeUpdate,
	PermMetricsRead,
	PermMonitorsRead,
	PermMonitorsManage,
	PermContainersRead,
	PermContainersAction,
	PermContainersDeploy,
	PermContainersSettings,
	PermTerminalExec,
	PermFilesRead,
	PermFilesWrite,
	PermFilesDelete,
	PermFilesShare,
	PermJobsRead,
	PermJobsRun,
	PermJobsManage,
	PermLogsRead,
	PermLogsManage,
	PermNetworkRead,
	PermNetworkManage,
	PermBackupsRead,
	PermBackupsCreate,
	PermBackupsRestore,
	PermBackupsManage,
	PermVMsRead,
	PermVMsAction,
	PermVMsManage,
	PermAlertsRead,
	PermAlertsManage,
}

// PermissionItem describes a single granular permission for UI presentation.
type PermissionItem struct {
	Key         string `json:"key"`
	Label       string `json:"label"`
	Description string `json:"description"`
	Danger      bool   `json:"danger,omitempty"`
}

// PermissionModuleGroup groups related permissions by feature module.
type PermissionModuleGroup struct {
	Module      string           `json:"module"`
	Label       string           `json:"label"`
	Description string           `json:"description"`
	Permissions []PermissionItem `json:"permissions"`
}

// RoleOption describes a global role or node preset for the UI.
type RoleOption struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Permissions []string `json:"permissions,omitempty"`
}

// PermissionsCatalog is returned by GET /v1/users/permissions-catalog.
type PermissionsCatalog struct {
	GlobalRoles []RoleOption            `json:"global_roles"`
	NodePresets []RoleOption            `json:"node_presets"`
	Modules     []PermissionModuleGroup `json:"modules"`
}

// GetPermissionsCatalog returns the full metadata catalog of roles, presets, and granular permissions.
func GetPermissionsCatalog() *PermissionsCatalog {
	return &PermissionsCatalog{
		GlobalRoles: []RoleOption{
			{
				ID:          RoleAdmin,
				Name:        "Administrator",
				Description: "Full access to the entire platform, user management, projects, and all nodes.",
				Permissions: []string{PermWildcard},
			},
			{
				ID:          RoleMember,
				Name:        "Member",
				Description: "Scoped access to assigned projects and granular permissions per node.",
			},
		},
		NodePresets: []RoleOption{
			{
				ID:          NodePresetViewer,
				Name:        "Viewer",
				Description: "Read-only access to metrics, containers, files, logs, network, backups, and VMs.",
				Permissions: PresetViewerPermissions,
			},
			{
				ID:          NodePresetOperator,
				Name:        "Operator",
				Description: "Read + start/restart containers and VMs, upload files, run commands, and create backups.",
				Permissions: PresetOperatorPermissions,
			},
			{
				ID:          NodePresetEditor,
				Name:        "Editor / DevOps",
				Description: "Full node control including terminal, deployments, network routes, restores, and alerts.",
				Permissions: PresetEditorPermissions,
			},
			{
				ID:          NodePresetCustom,
				Name:        "Custom",
				Description: "Manual granular selection of each permission or action allowed on the node.",
				Permissions: []string{PermNodeRead},
			},
		},
		Modules: []PermissionModuleGroup{
			{
				Module:      "node",
				Label:       "General Node",
				Description: "Visibility and general server information.",
				Permissions: []PermissionItem{
					{Key: PermNodeRead, Label: "View node", Description: "View general status, IP, OS, and node summary."},
					{Key: PermNodeUpdate, Label: "Edit node", Description: "Change hostname or basic node configuration."},
					{Key: PermNodeDelete, Label: "Delete node", Description: "Unlink and remove the node from the platform.", Danger: true},
				},
			},
			{
				Module:      "metrics",
				Label:       "Metrics & Monitors",
				Description: "Hardware telemetry (CPU, RAM, Disk, Temperature) and HTTP/TCP monitors.",
				Permissions: []PermissionItem{
					{Key: PermMetricsRead, Label: "View metrics", Description: "View real-time charts and resource usage history."},
					{Key: PermMonitorsRead, Label: "View monitors", Description: "View service monitor status and auto-healing events."},
					{Key: PermMonitorsManage, Label: "Manage monitors", Description: "Create, edit, and delete HTTP/TCP monitors and auto-healing rules."},
				},
			},
			{
				Module:      "containers",
				Label:       "Containers & Marketplace",
				Description: "Docker container management and application deployment.",
				Permissions: []PermissionItem{
					{Key: PermContainersRead, Label: "View containers", Description: "List running containers and their metrics."},
					{Key: PermContainersAction, Label: "Control containers", Description: "Start, stop, and restart existing containers."},
					{Key: PermContainersDeploy, Label: "Deploy applications", Description: "Install apps from the Marketplace or deploy Docker Compose stacks."},
					{Key: PermContainersSettings, Label: "Container settings", Description: "Lock/unlock containers and modify their configuration."},
				},
			},
			{
				Module:      "terminal",
				Label:       "Interactive Terminal",
				Description: "Direct remote shell console on the host server.",
				Permissions: []PermissionItem{
					{Key: PermTerminalExec, Label: "Access terminal", Description: "Open interactive terminal (shell) sessions on the node.", Danger: true},
				},
			},
			{
				Module:      "files",
				Label:       "Files & Shared Folders",
				Description: "Node file explorer, transfers, and public links.",
				Permissions: []PermissionItem{
					{Key: PermFilesRead, Label: "Read & download files", Description: "Browse directories, download files, and stream media."},
					{Key: PermFilesWrite, Label: "Upload files & create folders", Description: "Upload files, create directories, and transfer files to the node."},
					{Key: PermFilesDelete, Label: "Delete files", Description: "Delete files and folders from the node filesystem.", Danger: true},
					{Key: PermFilesShare, Label: "Share folders (Links / Funnel)", Description: "Create, edit, and revoke password-protected public links."},
				},
			},
			{
				Module:      "jobs",
				Label:       "Commands, Jobs & Scheduled Tasks",
				Description: "Script execution, remote jobs, and cron jobs.",
				Permissions: []PermissionItem{
					{Key: PermJobsRead, Label: "View jobs & tasks", Description: "View execution history, available commands, and scheduled tasks."},
					{Key: PermJobsRun, Label: "Run commands", Description: "Trigger commands and jobs on the node."},
					{Key: PermJobsManage, Label: "Manage scheduled tasks", Description: "Create, edit, and delete scheduled tasks (cron) on the node."},
				},
			},
			{
				Module:      "logs",
				Label:       "Logs & AI Diagnostics",
				Description: "System log streaming and automated incident analysis.",
				Permissions: []PermissionItem{
					{Key: PermLogsRead, Label: "View logs & diagnostics", Description: "View real-time logs and diagnostic reports."},
					{Key: PermLogsManage, Label: "Run diagnostics", Description: "Request log collection, AI analysis, and manage diagnostics."},
				},
			},
			{
				Module:      "network",
				Label:       "Network, Reverse Proxy & Tunnels",
				Description: "Network stats, Caddy routes, Cloudflare Tunnel, and Tailscale Funnel.",
				Permissions: []PermissionItem{
					{Key: PermNetworkRead, Label: "View network & routes", Description: "View network interfaces, listening ports, Caddy routes, and tunnels."},
					{Key: PermNetworkManage, Label: "Manage routes & tunnels", Description: "Create, modify, and delete Caddy routes, Cloudflare Tunnel, and Funnel."},
				},
			},
			{
				Module:      "backups",
				Label:       "Backups",
				Description: "Local/cloud backups and container restoration.",
				Permissions: []PermissionItem{
					{Key: PermBackupsRead, Label: "View backups", Description: "View backup history and current configuration."},
					{Key: PermBackupsCreate, Label: "Create & download backups", Description: "Run manual backups and download backup files."},
					{Key: PermBackupsRestore, Label: "Restore backups", Description: "Overwrite and restore container data from a backup.", Danger: true},
					{Key: PermBackupsManage, Label: "Configure backup policies", Description: "Create or modify automatic schedules and backup retention."},
				},
			},
			{
				Module:      "vms",
				Label:       "Virtual Machines (KVM / QEMU)",
				Description: "Hypervisor and virtual machine management on the node.",
				Permissions: []PermissionItem{
					{Key: PermVMsRead, Label: "View hypervisor & VMs", Description: "Check KVM capacity and virtual machine status."},
					{Key: PermVMsAction, Label: "Control VM power", Description: "Start, stop, and restart existing virtual machines."},
					{Key: PermVMsManage, Label: "Create & delete VMs", Description: "Provision new virtual machines or destroy existing ones.", Danger: true},
				},
			},
			{
				Module:      "alerts",
				Label:       "Node Alerts",
				Description: "Notification rules for CPU, RAM, Disk, and offline thresholds.",
				Permissions: []PermissionItem{
					{Key: PermAlertsRead, Label: "View alerts", Description: "View threshold settings and triggered alert history."},
					{Key: PermAlertsManage, Label: "Configure alerts", Description: "Modify alert thresholds and node notification channels."},
				},
			},
			{
				Module:      "connectors",
				Label:       "Connectors",
				Description: "Saved requests to external APIs. Project-level: taken from the project membership, not from per-node grants.",
				Permissions: []PermissionItem{
					{Key: PermConnectorsRead, Label: "View connectors", Description: "View the project's connectors and the data their queries return."},
					{Key: PermConnectorsRun, Label: "Run connector actions", Description: "Trigger action connectors, using the credentials stored in them."},
					{Key: PermConnectorsManage, Label: "Manage connectors", Description: "Create, edit, and delete connectors and set their credentials.", Danger: true},
				},
			},
		},
	}
}

// IsValidGlobalRole checks whether role is "admin" or "member".
func IsValidGlobalRole(role string) bool {
	return role == RoleAdmin || role == RoleMember
}

// IsValidNodePreset checks whether preset is a recognized node role preset.
func IsValidNodePreset(preset string) bool {
	switch preset {
	case NodePresetViewer, NodePresetOperator, NodePresetEditor, NodePresetCustom:
		return true
	default:
		return false
	}
}

// PresetPermissions returns a copy of the canonical permission slice for a preset.
func PresetPermissions(preset string) []string {
	switch preset {
	case NodePresetViewer:
		return slices.Clone(PresetViewerPermissions)
	case NodePresetOperator:
		return slices.Clone(PresetOperatorPermissions)
	case NodePresetEditor:
		return slices.Clone(PresetEditorPermissions)
	case NodePresetAdmin:
		return []string{PermWildcard}
	default:
		return []string{PermNodeRead}
	}
}

// NormalizePermissions validates, deduplicates, and expands implied read permissions.
func NormalizePermissions(perms []string) []string {
	validSet := make(map[string]bool, len(AllNodePermissions))
	for _, p := range AllNodePermissions {
		validSet[p] = true
	}

	granted := make(map[string]bool)
	for _, raw := range perms {
		p := strings.ToLower(strings.TrimSpace(raw))
		if p == PermWildcard {
			return []string{PermWildcard}
		}
		if validSet[p] {
			granted[p] = true
		}
	}

	// Any node permission implies node:read so the user can see the node in their list
	granted[PermNodeRead] = true

	// Module-level write/action/manage permissions automatically imply module:read
	if granted[PermMonitorsManage] {
		granted[PermMonitorsRead] = true
	}
	if granted[PermContainersAction] || granted[PermContainersDeploy] || granted[PermContainersSettings] {
		granted[PermContainersRead] = true
	}
	if granted[PermFilesWrite] || granted[PermFilesDelete] || granted[PermFilesShare] {
		granted[PermFilesRead] = true
	}
	if granted[PermJobsRun] || granted[PermJobsManage] {
		granted[PermJobsRead] = true
	}
	if granted[PermLogsManage] {
		granted[PermLogsRead] = true
	}
	if granted[PermNetworkManage] {
		granted[PermNetworkRead] = true
	}
	if granted[PermBackupsCreate] || granted[PermBackupsRestore] || granted[PermBackupsManage] {
		granted[PermBackupsRead] = true
	}
	if granted[PermVMsAction] || granted[PermVMsManage] {
		granted[PermVMsRead] = true
	}
	if granted[PermAlertsManage] {
		granted[PermAlertsRead] = true
	}
	if granted[PermConnectorsManage] {
		granted[PermConnectorsRun] = true
	}
	if granted[PermConnectorsRun] {
		granted[PermConnectorsRead] = true
	}

	// Return in canonical order
	result := make([]string, 0, len(granted))
	for _, p := range AllNodePermissions {
		if granted[p] {
			result = append(result, p)
		}
	}
	return result
}

// ResolveNodePermissions determines the effective role_preset and normalized permission list.
func ResolveNodePermissions(preset string, customPerms []string) (string, []string) {
	preset = strings.ToLower(strings.TrimSpace(preset))
	if !IsValidNodePreset(preset) {
		if len(customPerms) > 0 {
			preset = NodePresetCustom
		} else {
			preset = NodePresetViewer
		}
	}

	if preset != NodePresetCustom && len(customPerms) == 0 {
		return preset, PresetPermissions(preset)
	}

	normalized := NormalizePermissions(customPerms)
	if preset != NodePresetCustom {
		// Check if the provided customPerms match the preset exactly; if not, mark as custom
		presetDefault := PresetPermissions(preset)
		if !slices.Equal(normalized, presetDefault) {
			preset = NodePresetCustom
		}
	}
	return preset, normalized
}

// HasPermission checks if a granted permission slice satisfies the required permission.
func HasPermission(granted []string, required string) bool {
	if required == "" {
		return len(granted) > 0
	}
	for _, p := range granted {
		if p == PermWildcard || p == required {
			return true
		}
	}
	// Check implied read permissions even if raw slice wasn't normalized
	expanded := NormalizePermissions(granted)
	return slices.Contains(expanded, required)
}

// ProjectRolePermissions returns the project-level permissions (connectors) granted by a membership. The
// named presets map to fixed sets, so memberships saved before these permissions existed get them too;
// "custom" uses the stored permissions.
func ProjectRolePermissions(role string, perms []string) []string {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case NodePresetAdmin:
		return []string{PermWildcard}
	case NodePresetViewer:
		return []string{PermConnectorsRead}
	case NodePresetOperator:
		return []string{PermConnectorsRead, PermConnectorsRun}
	case NodePresetEditor:
		return []string{PermConnectorsRead, PermConnectorsRun, PermConnectorsManage}
	default:
		var out []string
		for _, p := range NormalizePermissions(perms) {
			if p == PermWildcard || strings.HasPrefix(p, "connectors:") {
				out = append(out, p)
			}
		}
		return out
	}
}
