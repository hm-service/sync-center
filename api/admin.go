package api

import (
	"crypto/subtle"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"sync-center/app"
	"sync-center/core"
	"sync-center/httpx"
)

const adminPageSize = 20

func (s *Server) adminNamespaces(w http.ResponseWriter, r *http.Request) {
	items := make([]map[string]any, 0, len(s.app.Namespaces))
	for _, name := range s.app.Namespaces {
		info, damaged, err := s.app.Snapshots.Info(name)
		if err != nil {
			s.logger.Error("read namespace", "namespace", name, "error", err)
			s.fail(w, r, http.StatusInternalServerError, "internal", "error.internal")
			return
		}
		s.warnDamaged(name, damaged)
		items = append(items, map[string]any{
			"name":      info.Name,
			"createdAt": info.CreatedAt,
			"updatedAt": info.UpdatedAt,
		})
	}
	httpx.JSON(w, http.StatusOK, items)
}

func (s *Server) adminSnapshots(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !s.app.HasNamespace(name) {
		s.fail(w, r, http.StatusNotFound, "namespace_not_found", "error.namespace-not-found")
		return
	}
	limit := adminPageSize
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 100 {
			s.fail(w, r, http.StatusUnprocessableEntity, "invalid_limit", "error.invalid-limit")
			return
		}
		limit = parsed
	}
	metas, damaged, err := s.app.Snapshots.List(name, limit)
	if err != nil {
		s.logger.Error("list snapshots", "namespace", name, "error", err)
		s.fail(w, r, http.StatusInternalServerError, "internal", "error.internal")
		return
	}
	s.warnDamaged(name, damaged)
	httpx.JSON(w, http.StatusOK, map[string]any{"namespace": name, "snapshots": snapshotMetas(metas)})
}

func (s *Server) adminSnapshot(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !s.app.HasNamespace(name) {
		s.fail(w, r, http.StatusNotFound, "namespace_not_found", "error.namespace-not-found")
		return
	}
	seq, err := strconv.ParseInt(r.PathValue("seq"), 10, 64)
	if err != nil || seq < 1 {
		s.fail(w, r, http.StatusNotFound, "snapshot_not_found", "error.snapshot-not-found")
		return
	}
	s.serveSnapshot(w, r, name, seq, "snapshot_not_found", "error.snapshot-not-found")
}

func (s *Server) adminBlob(w http.ResponseWriter, r *http.Request) {
	// digest 参与拼接存储路径，先校验再落盘，避免 ../ 之类的路径穿越
	digest := r.PathValue("digest")
	if !core.IsHash(digest) {
		s.fail(w, r, http.StatusNotFound, "blob_not_found", "error.blob-not-found")
		return
	}
	path := s.app.Blobs.Path(digest)
	if _, err := os.Stat(path); err != nil {
		s.fail(w, r, http.StatusNotFound, "blob_not_found", "error.blob-not-found")
		return
	}
	mediaType := "application/octet-stream"
	if name := r.URL.Query().Get("name"); name != "" {
		if guessed := mime.TypeByExtension(filepath.Ext(name)); guessed != "" {
			mediaType = guessed
		}
	}
	w.Header().Set("Content-Type", mediaType)
	http.ServeFile(w, r, path)
}

// collectGarbage 按需触发一次回收；正在回收时返回 423，不排队。
// 回收跨 namespace，无法用 namespace token 授权，只认 GC_TOKEN。
func (s *Server) collectGarbage(w http.ResponseWriter, r *http.Request) {
	if !s.gcAuthorized(r) {
		s.fail(w, r, http.StatusUnauthorized, "unauthorized", "error.unauthorized")
		return
	}
	started := time.Now()
	removed, ran, err := s.app.TryCollectGarbage(app.GarbageGrace)
	if err != nil {
		s.logger.Error("collect garbage", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "internal", "error.internal")
		return
	}
	if !ran {
		s.fail(w, r, http.StatusLocked, "gc_running", "error.gc-running")
		return
	}
	s.logger.Info("garbage collected", "removed", removed)
	httpx.JSON(w, http.StatusOK, map[string]any{
		"removed":    removed,
		"durationMs": time.Since(started).Milliseconds(),
	})
}

// gcAuthorized 比对摘要而不是明文，与 namespace token 的处理保持一致。
func (s *Server) gcAuthorized(r *http.Request) bool {
	header := r.Header.Get("Authorization")
	if !strings.HasPrefix(header, "Bearer ") {
		return false
	}
	expected := s.app.Config.GCTokenHash
	provided := core.Digest(strings.TrimPrefix(header, "Bearer "))
	return subtle.ConstantTimeCompare(expected[:], provided[:]) == 1
}
