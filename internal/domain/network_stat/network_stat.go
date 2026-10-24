// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package networkstat

import "time"

// InterfaceStat mirrors the agent's sysinfo.NetworkInterfaceStats.
type InterfaceStat struct {
	Interface string `json:"interface"`
	RxBytes   int64  `json:"rx_bytes"`
	RxPackets int64  `json:"rx_packets"`
	RxErrors  int64  `json:"rx_errors"`
	RxDropped int64  `json:"rx_dropped"`
	TxBytes   int64  `json:"tx_bytes"`
	TxPackets int64  `json:"tx_packets"`
	TxErrors  int64  `json:"tx_errors"`
	TxDropped int64  `json:"tx_dropped"`
}

// ListeningPort mirrors the agent's sysinfo.ListeningPort.
type ListeningPort struct {
	Port     int    `json:"port"`
	Protocol string `json:"protocol"`
	BindAddr string `json:"bind_addr"`
}

// VPNPeer mirrors the agent's sysinfo.VPNPeer active tunnel status.
type VPNPeer struct {
	IP        string  `json:"ip"`
	Hostname  string  `json:"hostname"`
	Online    bool    `json:"online"`
	LatencyMs float64 `json:"latency_ms"`
	Relay     string  `json:"relay,omitempty"`
}

// ActiveConnection mirrors the agent's sysinfo.ActiveConnection.
type ActiveConnection struct {
	LocalIP    string `json:"local_ip"`
	LocalPort  uint16 `json:"local_port"`
	RemoteIP   string `json:"remote_ip"`
	RemotePort uint16 `json:"remote_port"`
	Protocol   string `json:"protocol"`
	State      string `json:"state"`
}

// HTTPRequestLog mirrors the agent's sysinfo.HTTPRequestLog.
type HTTPRequestLog struct {
	Timestamp  string  `json:"timestamp"`
	ClientIP   string  `json:"client_ip"`
	Method     string  `json:"method"`
	Path       string  `json:"path"`
	StatusCode int     `json:"status_code"`
	DurationMs float64 `json:"duration_ms"`
	UserAgent  string  `json:"user_agent,omitempty"`
}

// NodeNetworkStats is the stored snapshot for a node.
type NodeNetworkStats struct {
	ID          string             `json:"id"           db:"id"`
	NodeID      string             `json:"node_id"      db:"node_id"`
	CollectedAt time.Time          `json:"collected_at" db:"collected_at"`
	RxBytes     int64              `json:"rx_bytes"     db:"rx_bytes"`
	TxBytes     int64              `json:"tx_bytes"     db:"tx_bytes"`
	RxDropped   int64              `json:"rx_dropped"   db:"rx_dropped"`
	TxDropped   int64              `json:"tx_dropped"   db:"tx_dropped"`
	RxErrors    int64              `json:"rx_errors"    db:"rx_errors"`
	TxErrors    int64              `json:"tx_errors"    db:"tx_errors"`
	RPS         float64            `json:"rps"          db:"rps"`
	Interfaces  []InterfaceStat    `json:"interfaces"`
	Ports       []ListeningPort    `json:"ports"`
	Peers       []VPNPeer          `json:"peers"`
	Connections []ActiveConnection `json:"connections"`
	Requests    []HTTPRequestLog   `json:"requests"`
}

// IngestPayload is the JSON body the agent sends.
type IngestPayload struct {
	Interfaces  []InterfaceStat    `json:"interfaces"`
	Ports       []ListeningPort    `json:"ports"`
	Peers       []VPNPeer          `json:"peers"`
	Connections []ActiveConnection `json:"connections"`
	Requests    []HTTPRequestLog   `json:"requests"`
	RPS         float64            `json:"rps"`
}
