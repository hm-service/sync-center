package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strconv"

	"sync-center/core"
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
	lease, ok := s.app.Leases.Acquire(p.Namespace)
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
	lease, ok := s.app.Leases.Heartbeat(r.PathValue("id"), p.Namespace)
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
	if !s.app.Leases.Release(r.PathValue("id"), p.Namespace) {
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
		// 已存在也要把请求体读完，否则这条连接无法复用；但同样受大小上限约束
		r.Body = http.MaxBytesReader(w, r.Body, core.MaxBlobSize+1)
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
				s.fail(w, r, http.StatusRequestEntityTooLarge, "too_large", "error.too-large")
				return
			}
			s.fail(w, r, http.StatusBadRequest, "invalid_body", "error.invalid-body")
			return
		}
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
	// 204 响应不能带 Content-Length，HEAD 只表示存在性
	if _, exists := s.app.Blobs.Stat(hash); !exists {
		s.fail(w, r, http.StatusNotFound, "not_found", "error.blob-not-found")
		return
	}
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
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			s.fail(w, r, http.StatusRequestEntityTooLarge, "too_large", "error.too-large")
			return
		}
		s.fail(w, r, http.StatusUnprocessableEntity, "invalid_body", "error.invalid-body")
		return
	}
	if request.LeaseID == "" {
		s.fail(w, r, http.StatusUnprocessableEntity, "invalid_body", "error.lease-id-required")
		return
	}
	if _, held := s.app.Leases.Peek(request.LeaseID, p.Namespace); !held {
		s.fail(w, r, http.StatusLocked, "lease_required", "error.lease-required")
		return
	}

	manifest, total, err := core.Normalize(request.Manifest)
	if err != nil {
		s.fail(w, r, http.StatusUnprocessableEntity, "invalid_manifest", "error.invalid-manifest", err.Error())
		return
	}
	seq, err := s.app.Snapshots.Commit(p.Namespace, request.Parent, manifest, total, s.blobCheck(manifest))
	if errors.Is(err, core.ErrConflict) {
		// parent 已过期，本次提交注定失败；释放租约让客户端重新拉取后立即重试
		s.app.Leases.Release(request.LeaseID, p.Namespace)
		s.fail(w, r, http.StatusConflict, "conflict", "error.conflict")
		return
	}
	var check *blobCheckError
	if errors.As(err, &check) {
		s.fail(w, r, http.StatusUnprocessableEntity, check.code, check.key, check.hash)
		return
	}
	if err != nil {
		s.logger.Error("commit snapshot", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "internal", "error.internal")
		return
	}
	s.app.Leases.Release(request.LeaseID, p.Namespace)
	httpx.JSON(w, http.StatusCreated, map[string]any{"seq": seq})
}

// blobCheck 在提交的共享锁内执行：blob 必须仍在，且大小与 manifest 一致。
// 放在锁内是为了让回收无法在"校验通过"与"快照落盘"之间删掉被引用的 blob。
func (s *Server) blobCheck(manifest core.Manifest) func() error {
	return func() error {
		for _, entry := range manifest.Files {
			size, exists := s.app.Blobs.Stat(entry.Hash)
			if !exists {
				return &blobCheckError{code: "missing_blob", key: "error.missing-blob", hash: entry.Hash}
			}
			if size != entry.Size {
				return &blobCheckError{code: "blob_size_mismatch", key: "error.blob-size-mismatch", hash: entry.Hash}
			}
		}
		return nil
	}
}

// blobCheckError 让 handler 把校验失败映射成对应的 422 错误码。
type blobCheckError struct {
	code string
	key  string
	hash string
}

func (e *blobCheckError) Error() string { return e.code + ": " + e.hash }

func (s *Server) latestSnapshot(w http.ResponseWriter, r *http.Request) {
	p, ok := s.require(w, r, false)
	if !ok {
		return
	}
	metas, damaged, err := s.app.Snapshots.List(p.Namespace, 1)
	if err != nil {
		s.logger.Error("read head", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "internal", "error.internal")
		return
	}
	s.warnDamaged(p.Namespace, damaged)
	var head int64
	if len(metas) > 0 {
		head = metas[0].Seq
	}
	if core.DamagedAhead(damaged, head) {
		// 更新的快照读不出来时不能把旧版本当作 latest，否则客户端会拉回旧状态
		s.logger.Error("newest snapshot is damaged", "namespace", p.Namespace)
		s.fail(w, r, http.StatusInternalServerError, "internal", "error.internal")
		return
	}
	if len(metas) == 0 {
		s.fail(w, r, http.StatusNotFound, "not_found", "error.no-snapshot")
		return
	}
	s.serveSnapshot(w, r, p.Namespace, metas[0].Seq, "not_found", "error.no-snapshot")
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
	metas, damaged, err := s.app.Snapshots.List(p.Namespace, limit)
	if err != nil {
		s.logger.Error("list snapshots", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "internal", "error.internal")
		return
	}
	s.warnDamaged(p.Namespace, damaged)
	httpx.JSON(w, http.StatusOK, map[string]any{"snapshots": snapshotMetas(metas)})
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
	s.serveSnapshot(w, r, p.Namespace, seq, "not_found", "error.snapshot-not-found")
}

func (s *Server) metadata(w http.ResponseWriter, r *http.Request) {
	p, ok := s.require(w, r, false)
	if !ok {
		return
	}
	metas, damaged, err := s.app.Snapshots.List(p.Namespace, metadataPage)
	if err != nil {
		s.logger.Error("read metadata", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "internal", "error.internal")
		return
	}
	s.warnDamaged(p.Namespace, damaged)
	var head int64
	if len(metas) > 0 {
		head = metas[0].Seq
	}
	if core.DamagedAhead(damaged, head) {
		s.logger.Error("newest snapshot is damaged", "namespace", p.Namespace)
		s.fail(w, r, http.StatusInternalServerError, "internal", "error.internal")
		return
	}
	mode := "r"
	if p.CanWrite {
		mode = "rw"
	}
	httpx.JSON(w, http.StatusOK, map[string]any{
		"namespace": p.Namespace,
		"mode":      mode,
		"snapshots": snapshotMetas(metas),
	})
}

// serveSnapshot 打开快照并流式写出，文件本身已是响应结构，不做解析。
func (s *Server) serveSnapshot(w http.ResponseWriter, r *http.Request, namespace string, seq int64, code, key string) {
	snapshot, err := s.app.Snapshots.Open(namespace, seq)
	if err != nil {
		s.logger.Error("read snapshot", "namespace", namespace, "seq", seq, "error", err)
		s.fail(w, r, http.StatusInternalServerError, "internal", "error.internal")
		return
	}
	if snapshot == nil {
		s.fail(w, r, http.StatusNotFound, code, key)
		return
	}
	defer snapshot.Close()
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if err := snapshot.WriteJSON(w); err != nil {
		s.logger.Error("write snapshot", "namespace", namespace, "seq", seq, "error", err)
	}
}

func (s *Server) warnDamaged(namespace string, damaged []string) {
	for _, name := range damaged {
		s.logger.Warn("damaged snapshot skipped", "namespace", namespace, "file", name)
	}
}

func snapshotMetas(metas []core.SnapshotMeta) []snapshotMetaJSON {
	items := make([]snapshotMetaJSON, 0, len(metas))
	for _, meta := range metas {
		items = append(items, snapshotMetaJSON{Seq: meta.Seq, CreatedAt: meta.CreatedAt, TotalBytes: meta.TotalBytes})
	}
	return items
}
