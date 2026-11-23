// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Arturo (AutoHost)

package handler

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"github.com/arturo/autohost-cloud-api/internal/core/identity"
	"github.com/arturo/autohost-cloud-api/internal/filetransfer"
)

// Text file endpoints for clients that only speak JSON (scripts, MCP clients), which cannot send multipart
// uploads or read binary downloads. Binary or large files keep using /upload and /download.

// maxTextFileBytes bounds the content read or written through the text endpoints. It keeps a JSON request
// (content plus escaping) under the 2 MB body limit.
const maxTextFileBytes = 1 << 20

// remoteTextReadTimeout bounds reading a text file from an agent.
const remoteTextReadTimeout = 2 * time.Minute

type writeFileContentRequest struct {
	Path      string  `json:"path"`
	Content   *string `json:"content"`
	Overwrite bool    `json:"overwrite"`
}

// WriteFileContent creates (or, with overwrite, replaces) a UTF-8 text file in the node's shared folder.
// PUT /v1/nodes/{nodeId}/files/content
func (h *NodeFileHandler) WriteFileContent(w http.ResponseWriter, r *http.Request) {
	targetNode, ok := h.authorizeNode(w, r, chi.URLParam(r, "nodeId"), identity.PermFilesWrite)
	if !ok {
		return
	}
	var req writeFileContentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeFileError(w, "invalid json body", http.StatusBadRequest)
		return
	}
	dir, name, err := splitTextFilePath(req.Path)
	if err != nil {
		writeFileError(w, err.Error(), http.StatusBadRequest)
		return
	}
	if req.Content == nil {
		writeFileError(w, "content is required (it may be an empty string)", http.StatusBadRequest)
		return
	}
	if len(*req.Content) > maxTextFileBytes {
		writeFileError(w, fmt.Sprintf("content is larger than %d bytes: use the multipart upload", maxTextFileBytes), http.StatusRequestEntityTooLarge)
		return
	}

	existing, err := h.files.ListNodeSharedFileNames(r.Context(), targetNode, dir)
	if err != nil {
		if filetransfer.KindOf(err) == filetransfer.KindInternal {
			// Unclassified errors (agent offline or timing out, local I/O): log them, do not echo them.
			log.Printf("[ERROR] list %s on node %s before writing: %v", dir, targetNode.ID, err)
			writeFileError(w, "could not check whether the file exists on the node", http.StatusBadGateway)
			return
		}
		writeFileOpError(w, err)
		return
	}
	_, exists := existing[name]
	if exists && !req.Overwrite {
		writeFileError(w, "file already exists (set overwrite to true to replace it)", http.StatusConflict)
		return
	}

	relPath, written, err := h.files.Upload(r.Context(), targetNode, dir, name, strings.NewReader(*req.Content), h.ResolveMasterBaseURL(r))
	if err != nil {
		writeFileOpError(w, err)
		return
	}
	status := http.StatusCreated
	if exists {
		status = http.StatusOK
	}
	writeFileJSON(w, status, map[string]any{"path": relPath, "size_bytes": written, "overwritten": exists})
}

// ReadFileContent returns a text file of the node's shared folder as a JSON string.
// GET /v1/nodes/{nodeId}/files/content?path=
func (h *NodeFileHandler) ReadFileContent(w http.ResponseWriter, r *http.Request) {
	targetNode, ok := h.authorizeNode(w, r, chi.URLParam(r, "nodeId"), identity.PermFilesRead)
	if !ok {
		return
	}
	relPath := r.URL.Query().Get("path")
	if _, _, err := splitTextFilePath(relPath); err != nil {
		writeFileError(w, err.Error(), http.StatusBadRequest)
		return
	}

	var body io.Reader
	if targetNode.IsLocal {
		abs, err := h.files.LocalFile(relPath)
		if err != nil {
			writeFileOpError(w, err)
			return
		}
		f, err := os.Open(abs)
		if err != nil {
			writeFileError(w, "file not found", http.StatusNotFound)
			return
		}
		defer f.Close()
		body = f
	} else {
		stream, err := h.files.OpenRemote(r.Context(), targetNode, relPath, h.ResolveMasterBaseURL(r), 0, remoteTextReadTimeout)
		if err != nil {
			writeFileOpError(w, err)
			return
		}
		defer stream.Close()
		if stream.ContentLen > maxTextFileBytes {
			writeTextTooLarge(w)
			return
		}
		body = stream.Body
	}

	// Read one byte past the limit to detect larger files without loading them.
	data, err := io.ReadAll(io.LimitReader(body, maxTextFileBytes+1))
	if err != nil {
		writeFileError(w, "could not read file", http.StatusBadGateway)
		return
	}
	if len(data) > maxTextFileBytes {
		writeTextTooLarge(w)
		return
	}
	if !isText(data) {
		writeFileError(w, "file is not UTF-8 text: use /download", http.StatusUnsupportedMediaType)
		return
	}
	writeFileJSON(w, http.StatusOK, map[string]any{
		"path":       path.Clean("/" + relPath),
		"size_bytes": len(data),
		"content":    string(data),
	})
}

func writeTextTooLarge(w http.ResponseWriter) {
	writeFileError(w, fmt.Sprintf("file is larger than %d bytes: use /download", maxTextFileBytes), http.StatusRequestEntityTooLarge)
}

// splitTextFilePath validates a file path relative to the shared folder and splits it into directory and
// file name. Path traversal is rejected later by the file service (and the agent).
func splitTextFilePath(p string) (dir, name string, err error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", "", fmt.Errorf("path is required")
	}
	if strings.HasSuffix(p, "/") {
		return "", "", fmt.Errorf("path must name a file, not a directory")
	}
	cleaned := path.Clean("/" + p)
	name = path.Base(cleaned)
	if name == "/" || name == "." || name == ".." {
		return "", "", fmt.Errorf("path must name a file")
	}
	return path.Dir(cleaned), name, nil
}

// isText reports whether data is valid UTF-8 without NUL bytes (a NUL almost always means binary data).
func isText(data []byte) bool {
	return utf8.Valid(data) && !bytes.ContainsRune(data, 0)
}
