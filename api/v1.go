package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"

	"sync-center/core"
	"sync-center/db"
	"sync-center/httpx"
)

const (
	maxSnapshotBody = 64 << 20
	defaultPageSize = 10
	maxPageSize     = 100
	metadataPage    = 5
)

type snapshotMetaJSON struct {
	Seq        int64 `json:"seq"`
	CreatedAt  int64 `json:"createdAt"`
	TotalBytes int64 `json:"totalBytes"`
}

type snapshotRequest struct {
	LeaseID  string          `json:"leaseId"`
	Parent   *int64          `json:"parent"`
	Manifest json.RawMessage `json:"manifest"`
}

func (s *Server) createLease(w http.ResponseWriter, r *http.Request) {
	p, ok := s.require(w, r, true)
	if !ok {
		return
	}
	lease, ok := s.app.Leases.Acquire(p.NamespaceID)
	if !ok {
		s.fail(w, r, http.StatusLocked, "leased", "error.leased")
		return
	}
	httpx.JSON(w, http.StatusCreated, map[string]any{
		"leaseId":   lease.ID,
		"expiresAt": lease.ExpiresAt.Unix(),
	})
}

func (s *Server) heartbeat(w http.ResponseWriter, r *http.Request) {
	p, ok := s.require(w, r, true)
	if !ok {
		return
	}
	lease, ok := s.app.Leases.Heartbeat(r.PathValue("id"), p.NamespaceID)
	if !ok {
		s.fail(w, r, http.StatusLocked, "lease_not_found", "error.lease-not-found")
		return
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"expiresAt": lease.ExpiresAt.Unix()})
}

func (s *Server) releaseLease(w http.ResponseWriter, r *http.Request) {
	p, ok := s.require(w, r, true)
	if !ok {
		return
	}
	if !s.app.Leases.Release(r.PathValue("id"), p.NamespaceID) {
		s.fail(w, r, http.StatusLocked, "lease_not_found", "error.lease-not-found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) putBlob(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.require(w, r, true); !ok {
		return
	}
	hash := r.PathValue("hash")
	if !core.IsHash(hash) {
		s.fail(w, r, http.StatusUnprocessableEntity, "invalid_digest", "error.invalid-digest")
		return
	}
	if _, exists := s.app.Blobs.Stat(hash); exists {
		// 已存在也要把请求体读完，否则这条连接无法复用
		_, _ = io.Copy(io.Discard, r.Body)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, core.MaxBlobSize+1)
	_, err := s.app.Blobs.Write(hash, r.Body)
	switch {
	case errors.Is(err, core.ErrBlobMismatch):
		s.fail(w, r, http.StatusUnprocessableEntity, "digest_mismatch", "error.digest-mismatch")
	case errors.Is(err, core.ErrBlobTooLarge):
		s.fail(w, r, http.StatusRequestEntityTooLarge, "too_large", "error.too-large")
	case err != nil:
		s.logger.Error("write blob", "hash", hash, "error", err)
		s.fail(w, r, http.StatusInternalServerError, "internal", "error.internal")
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}

func (s *Server) headBlob(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.require(w, r, false); !ok {
		return
	}
	hash := r.PathValue("hash")
	if !core.IsHash(hash) {
		s.fail(w, r, http.StatusUnprocessableEntity, "invalid_digest", "error.invalid-digest")
		return
	}
	size, exists := s.app.Blobs.Stat(hash)
	if !exists {
		s.fail(w, r, http.StatusNotFound, "not_found", "error.blob-not-found")
		return
	}
	w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) getBlob(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.require(w, r, false); !ok {
		return
	}
	hash := r.PathValue("hash")
	if !core.IsHash(hash) {
		s.fail(w, r, http.StatusUnprocessableEntity, "invalid_digest", "error.invalid-digest")
		return
	}
	path := s.app.Blobs.Path(hash)
	if _, err := os.Stat(path); err != nil {
		s.fail(w, r, http.StatusNotFound, "not_found", "error.blob-not-found")
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeFile(w, r, path)
}

func (s *Server) createSnapshot(w http.ResponseWriter, r *http.Request) {
	p, ok := s.require(w, r, true)
	if !ok {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxSnapshotBody)
	var request snapshotRequest
	if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
		s.fail(w, r, http.StatusUnprocessableEntity, "invalid_body", "error.invalid-body")
		return
	}
	if request.LeaseID == "" {
		s.fail(w, r, http.StatusUnprocessableEntity, "invalid_body", "error.lease-id-required")
		return
	}
	if _, held := s.app.Leases.Peek(request.LeaseID, p.NamespaceID); !held {
		s.fail(w, r, http.StatusLocked, "lease_required", "error.lease-required")
		return
	}

	payload, manifest, total, err := core.Normalize(request.Manifest)
	if err != nil {
		s.fail(w, r, http.StatusUnprocessableEntity, "invalid_manifest", "error.invalid-manifest", err.Error())
		return
	}
	for _, entry := range manifest.Files {
		size, exists := s.app.Blobs.Stat(entry.Hash)
		if !exists {
			s.fail(w, r, http.StatusUnprocessableEntity, "missing_blob", "error.missing-blob", entry.Hash)
			return
		}
		if size != entry.Size {
			s.fail(w, r, http.StatusUnprocessableEntity, "blob_size_mismatch", "error.blob-size-mismatch", entry.Hash)
			return
		}
	}

	seq, err := s.app.DB.Commit(p.NamespaceID, request.Parent, payload, total)
	if errors.Is(err, db.ErrConflict) {
		// parent 已过期，本次提交注定失败；释放租约让客户端重新拉取后立即重试
		s.app.Leases.Release(request.LeaseID, p.NamespaceID)
		s.fail(w, r, http.StatusConflict, "conflict", "error.conflict")
		return
	}
	if err != nil {
		s.logger.Error("commit snapshot", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "internal", "error.internal")
		return
	}
	s.app.Leases.Release(request.LeaseID, p.NamespaceID)
	httpx.JSON(w, http.StatusCreated, map[string]any{"seq": seq})
}

func (s *Server) latestSnapshot(w http.ResponseWriter, r *http.Request) {
	p, ok := s.require(w, r, false)
	if !ok {
		return
	}
	snapshot, err := s.app.DB.Head(p.NamespaceID)
	if err != nil {
		s.logger.Error("read head", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "internal", "error.internal")
		return
	}
	if snapshot == nil {
		s.fail(w, r, http.StatusNotFound, "not_found", "error.no-snapshot")
		return
	}
	s.writeSnapshot(w, snapshot)
}

func (s *Server) listSnapshots(w http.ResponseWriter, r *http.Request) {
	p, ok := s.require(w, r, false)
	if !ok {
		return
	}
	limit := defaultPageSize
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > maxPageSize {
			s.fail(w, r, http.StatusUnprocessableEntity, "invalid_limit", "error.invalid-limit")
			return
		}
		limit = parsed
	}
	rows, err := s.app.DB.Snapshots(p.NamespaceID, limit)
	if err != nil {
		s.logger.Error("list snapshots", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "internal", "error.internal")
		return
	}
	items := make([]snapshotMetaJSON, 0, len(rows))
	for _, row := range rows {
		items = append(items, snapshotMetaJSON{Seq: row.Seq, CreatedAt: row.CreatedAt, TotalBytes: row.TotalBytes})
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"snapshots": items})
}

func (s *Server) getSnapshot(w http.ResponseWriter, r *http.Request) {
	value := r.PathValue("seq")
	if value == "latest" {
		s.latestSnapshot(w, r)
		return
	}
	p, ok := s.require(w, r, false)
	if !ok {
		return
	}
	seq, err := strconv.ParseInt(value, 10, 64)
	if err != nil || seq < 1 {
		s.fail(w, r, http.StatusNotFound, "not_found", "error.snapshot-not-found")
		return
	}
	snapshot, err := s.app.DB.Snapshot(p.NamespaceID, seq)
	if err != nil {
		s.logger.Error("read snapshot", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "internal", "error.internal")
		return
	}
	if snapshot == nil {
		s.fail(w, r, http.StatusNotFound, "not_found", "error.snapshot-not-found")
		return
	}
	s.writeSnapshot(w, snapshot)
}

func (s *Server) metadata(w http.ResponseWriter, r *http.Request) {
	p, ok := s.require(w, r, false)
	if !ok {
		return
	}
	rows, err := s.app.DB.Snapshots(p.NamespaceID, metadataPage)
	if err != nil {
		s.logger.Error("read metadata", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "internal", "error.internal")
		return
	}
	mode := "r"
	if p.CanWrite {
		mode = "rw"
	}
	items := make([]snapshotMetaJSON, 0, len(rows))
	for _, row := range rows {
		items = append(items, snapshotMetaJSON{Seq: row.Seq, CreatedAt: row.CreatedAt, TotalBytes: row.TotalBytes})
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"namespace": p.Namespace,
		"mode":      mode,
		"snapshots": items,
	})
}

// writeSnapshot 直接把库里存的 manifest 原文拼进响应，避免解析成结构再序列化。
func (s *Server) writeSnapshot(w http.ResponseWriter, snapshot *db.Snapshot) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, "{\"seq\":%d,\"createdAt\":%d,\"totalBytes\":%d,\"manifest\":",
		snapshot.Seq, snapshot.CreatedAt, snapshot.TotalBytes)
	_, _ = io.WriteString(w, snapshot.Manifest)
	_, _ = io.WriteString(w, "}\n")
}
