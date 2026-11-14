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
				Name:        "Administrador",
				Description: "Acceso total a toda la plataforma, gestión de usuarios, proyectos y todos los nodos.",
				Permissions: []string{PermWildcard},
			},
			{
				ID:          RoleMember,
				Name:        "Miembro",
				Description: "Acceso acotado a proyectos asignados y permisos granulares por nodo.",
			},
		},
		NodePresets: []RoleOption{
			{
				ID:          NodePresetViewer,
				Name:        "Lector (Viewer)",
				Description: "Solo lectura de métricas, contenedores, archivos, logs, red, respaldos y VMs.",
				Permissions: PresetViewerPermissions,
			},
			{
				ID:          NodePresetOperator,
				Name:        "Operador (Operator)",
				Description: "Lectura + iniciar/reiniciar contenedores y VMs, subir archivos, ejecutar comandos y crear respaldos.",
				Permissions: PresetOperatorPermissions,
			},
			{
				ID:          NodePresetEditor,
				Name:        "Editor / DevOps",
				Description: "Control completo del nodo incluyendo terminal, despliegues, rutas de red, restauraciones y alertas.",
				Permissions: PresetEditorPermissions,
			},
			{
				ID:          NodePresetCustom,
				Name:        "Personalizado (Custom)",
				Description: "Selección manual y granular de cada permiso o acción permitida en el nodo.",
				Permissions: []string{PermNodeRead},
			},
		},
		Modules: []PermissionModuleGroup{
			{
				Module:      "node",
				Label:       "Nodo General",
				Description: "Visibilidad e información general del servidor.",
				Permissions: []PermissionItem{
					{Key: PermNodeRead, Label: "Ver nodo", Description: "Ver estado general, IP, sistema operativo y resumen del nodo."},
					{Key: PermNodeUpdate, Label: "Editar nodo", Description: "Cambiar hostname o configuración básica del nodo."},
					{Key: PermNodeDelete, Label: "Eliminar nodo", Description: "Desvincular y eliminar el nodo de la plataforma.", Danger: true},
				},
			},
			{
				Module:      "metrics",
				Label:       "Métricas y Monitores",
				Description: "Telemetría de hardware (CPU, RAM, Disco, Temperatura) y monitores HTTP/TCP.",
				Permissions: []PermissionItem{
					{Key: PermMetricsRead, Label: "Ver métricas", Description: "Ver gráficas en tiempo real e historial de uso de recursos."},
					{Key: PermMonitorsRead, Label: "Ver monitores", Description: "Ver estado de monitores de servicios y eventos de auto-healing."},
					{Key: PermMonitorsManage, Label: "Gestionar monitores", Description: "Crear, editar y eliminar monitores HTTP/TCP y reglas de auto-healing."},
				},
			},
			{
				Module:      "containers",
				Label:       "Contenedores y Marketplace",
				Description: "Gestión de contenedores Docker y despliegue de aplicaciones.",
				Permissions: []PermissionItem{
					{Key: PermContainersRead, Label: "Ver contenedores", Description: "Listar contenedores en ejecución y sus métricas."},
					{Key: PermContainersAction, Label: "Controlar contenedores", Description: "Iniciar, detener y reiniciar contenedores existentes."},
					{Key: PermContainersDeploy, Label: "Desplegar aplicaciones", Description: "Instalar apps desde el Marketplace o desplegar stacks Docker Compose."},
					{Key: PermContainersSettings, Label: "Ajustes de contenedor", Description: "Bloquear/desbloquear contenedores y modificar su configuración."},
				},
			},
			{
				Module:      "terminal",
				Label:       "Terminal Interactiva",
				Description: "Consola shell remota directa sobre el servidor host.",
				Permissions: []PermissionItem{
					{Key: PermTerminalExec, Label: "Acceder a terminal", Description: "Abrir sesiones interactivas de terminal (shell) en el nodo.", Danger: true},
				},
			},
			{
				Module:      "files",
				Label:       "Archivos y Carpetas Compartidas",
				Description: "Explorador de archivos del nodo, transferencias y enlaces públicos.",
				Permissions: []PermissionItem{
					{Key: PermFilesRead, Label: "Leer y descargar archivos", Description: "Explorar directorios, descargar archivos y reproducir multimedia."},
					{Key: PermFilesWrite, Label: "Subir archivos y crear carpetas", Description: "Subir archivos, crear directorios y transferir archivos al nodo."},
					{Key: PermFilesDelete, Label: "Eliminar archivos", Description: "Borrar archivos y carpetas del sistema de archivos del nodo.", Danger: true},
					{Key: PermFilesShare, Label: "Compartir carpetas (Links / Funnel)", Description: "Crear, editar y revocar enlaces públicos protegidos por contraseña."},
				},
			},
			{
				Module:      "jobs",
				Label:       "Comandos, Jobs y Tareas Programadas",
				Description: "Ejecución de scripts, trabajos remotos y cron jobs.",
				Permissions: []PermissionItem{
					{Key: PermJobsRead, Label: "Ver jobs y tareas", Description: "Ver historial de ejecuciones, comandos disponibles y tareas programadas."},
					{Key: PermJobsRun, Label: "Ejecutar comandos", Description: "Disparar comandos y trabajos (jobs) en el nodo."},
					{Key: PermJobsManage, Label: "Gestionar tareas programadas", Description: "Crear, editar y eliminar tareas programadas (cron) en el nodo."},
				},
			},
			{
				Module:      "logs",
				Label:       "Logs y Diagnósticos IA",
				Description: "Streaming de logs del sistema y análisis automatizado de incidentes.",
				Permissions: []PermissionItem{
					{Key: PermLogsRead, Label: "Ver logs y diagnósticos", Description: "Ver logs en tiempo real y reportes de diagnóstico."},
					{Key: PermLogsManage, Label: "Ejecutar diagnósticos", Description: "Solicitar recolección de logs, análisis IA y gestionar diagnósticos."},
				},
			},
			{
				Module:      "network",
				Label:       "Red, Proxy Inverso y Túneles",
				Description: "Estadísticas de red, rutas Caddy, Cloudflare Tunnel y Tailscale Funnel.",
				Permissions: []PermissionItem{
					{Key: PermNetworkRead, Label: "Ver red y rutas", Description: "Ver interfaces de red, puertos en escucha, rutas Caddy y túneles."},
					{Key: PermNetworkManage, Label: "Gestionar rutas y túneles", Description: "Crear, modificar y eliminar rutas Caddy, Cloudflare Tunnel y Funnel."},
				},
			},
			{
				Module:      "backups",
				Label:       "Respaldos (Backups)",
				Description: "Copias de seguridad locales/en la nube y restauración de contenedores.",
				Permissions: []PermissionItem{
					{Key: PermBackupsRead, Label: "Ver respaldos", Description: "Consultar historial de respaldos y configuración actual."},
					{Key: PermBackupsCreate, Label: "Crear y descargar respaldos", Description: "Ejecutar respaldos manuales y descargar archivos de respaldo."},
					{Key: PermBackupsRestore, Label: "Restaurar respaldos", Description: "Sobrescribir y restaurar datos de un contenedor desde un respaldo.", Danger: true},
					{Key: PermBackupsManage, Label: "Configurar políticas de respaldo", Description: "Crear o modificar programaciones automáticas y retención de respaldos."},
				},
			},
			{
				Module:      "vms",
				Label:       "Máquinas Virtuales (KVM / QEMU)",
				Description: "Gestión de hipervisor y máquinas virtuales sobre el nodo.",
				Permissions: []PermissionItem{
					{Key: PermVMsRead, Label: "Ver hipervisor y VMs", Description: "Consultar capacidad KVM y estado de las máquinas virtuales."},
					{Key: PermVMsAction, Label: "Controlar energía de VMs", Description: "Iniciar, detener y reiniciar máquinas virtuales existentes."},
					{Key: PermVMsManage, Label: "Crear y eliminar VMs", Description: "Aprovisionar nuevas máquinas virtuales o destruir existentes.", Danger: true},
				},
			},
			{
				Module:      "alerts",
				Label:       "Alertas del Nodo",
				Description: "Reglas de notificación por umbrales de CPU, RAM, Disco y desconexión.",
				Permissions: []PermissionItem{
					{Key: PermAlertsRead, Label: "Ver alertas", Description: "Consultar configuración de umbrales e historial de alertas disparadas."},
					{Key: PermAlertsManage, Label: "Configurar alertas", Description: "Modificar umbrales de alerta y canales de notificación del nodo."},
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
