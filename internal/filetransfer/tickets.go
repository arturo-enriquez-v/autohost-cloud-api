// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package filetransfer

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"io"
	"os"
	"sync"
	"time"

	"github.com/google/uuid"
)

// ticketTTL bounds how long an agent may use a transfer URL.
const ticketTTL = 60 * time.Minute

// Ticket authorizes one agent to pull from or push to the master over the internal-transfer endpoints
// (/v1/node-files/internal-transfer/{id}?token=...). A ticket either streams through a pipe (browser <->
// master <-> agent) or reads/writes a file on the master's disk.
type Ticket struct {
	ID        string
	Token     string
	FilePath  string // file on the master for file tickets
	ExpiresAt time.Time

	PipeReader *io.PipeReader // pipe tickets
	PipeWriter *io.PipeWriter

	started     chan struct{}
	startedOnce sync.Once
	mu          sync.Mutex
	contentLen  int64
	totalSize   int64
}

// IsPipe reports whether the ticket streams through a pipe rather than a file on disk.
func (t *Ticket) IsPipe() bool { return t.PipeReader != nil }

// MarkStarted signals that the agent connected.
func (t *Ticket) MarkStarted() { t.startedOnce.Do(func() { close(t.started) }) }

// Started is closed once the agent connected.
func (t *Ticket) Started() <-chan struct{} { return t.started }

// SetSizes records the Content-Length of the agent's upload and the full file size (for ranged streams).
func (t *Ticket) SetSizes(contentLen, totalSize int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.contentLen, t.totalSize = contentLen, totalSize
}

// Sizes returns the values recorded by SetSizes.
func (t *Ticket) Sizes() (contentLen, totalSize int64) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.contentLen, t.totalSize
}

// ReceiveFile writes an agent upload to the ticket's file on the master.
func (t *Ticket) ReceiveFile(body io.Reader) error {
	out, err := os.Create(t.FilePath)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, body)
	if closeErr := out.Close(); copyErr == nil {
		copyErr = closeErr
	}
	return copyErr
}

// ticketRegistry holds live tickets in memory (per process).
type ticketRegistry struct {
	mu      sync.RWMutex
	tickets map[string]*Ticket
}

func newTicketRegistry() *ticketRegistry {
	return &ticketRegistry{tickets: make(map[string]*Ticket)}
}

func (r *ticketRegistry) add(t *Ticket) *Ticket {
	raw := make([]byte, 24)
	_, _ = rand.Read(raw)
	t.ID = uuid.NewString()
	t.Token = hex.EncodeToString(raw)
	t.ExpiresAt = time.Now().Add(ticketTTL)
	t.started = make(chan struct{})
	r.mu.Lock()
	r.tickets[t.ID] = t
	r.mu.Unlock()
	return t
}

func (r *ticketRegistry) newFileTicket(filePath string) *Ticket {
	return r.add(&Ticket{FilePath: filePath})
}

func (r *ticketRegistry) newPipeTicket() *Ticket {
	pr, pw := io.Pipe()
	return r.add(&Ticket{PipeReader: pr, PipeWriter: pw})
}

func (r *ticketRegistry) remove(id string) {
	r.mu.Lock()
	delete(r.tickets, id)
	r.mu.Unlock()
}

// lookup returns a live ticket whose token matches (constant-time comparison).
func (r *ticketRegistry) lookup(id, token string) (*Ticket, bool) {
	r.mu.RLock()
	t, ok := r.tickets[id]
	r.mu.RUnlock()
	if !ok || t == nil || token == "" || time.Now().After(t.ExpiresAt) {
		return nil, false
	}
	if subtle.ConstantTimeCompare([]byte(t.Token), []byte(token)) != 1 {
		return nil, false
	}
	return t, true
}
