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

// NodeNetworkStats is the stored snapshot for a node.
type NodeNetworkStats struct {
	ID          string          `json:"id"           db:"id"`
	NodeID      string          `json:"node_id"      db:"node_id"`
	CollectedAt time.Time       `json:"collected_at" db:"collected_at"`
	RxBytes     int64           `json:"rx_bytes"     db:"rx_bytes"`
	TxBytes     int64           `json:"tx_bytes"     db:"tx_bytes"`
	RxDropped   int64           `json:"rx_dropped"   db:"rx_dropped"`
	TxDropped   int64           `json:"tx_dropped"   db:"tx_dropped"`
	RxErrors    int64           `json:"rx_errors"    db:"rx_errors"`
	TxErrors    int64           `json:"tx_errors"    db:"tx_errors"`
	Interfaces  []InterfaceStat `json:"interfaces"`
	Ports       []ListeningPort `json:"ports"`
}

// IngestPayload is the JSON body the agent sends.
type IngestPayload struct {
	Interfaces []InterfaceStat `json:"interfaces"`
	Ports      []ListeningPort `json:"ports"`
}
