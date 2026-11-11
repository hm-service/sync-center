package api

import (
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"

	"sync-center/core"
	"sync-center/httpx"
)

const uiPageSize = 20

func (s *Server) uiNamespaces(w http.ResponseWriter, r *http.Request) {
	rows, err := s.app.DB.ListNamespaces()
	if err != nil {
		s.logger.Error("list namespaces", "error", err)
		httpx.Fail(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	items := make([]map[string]any, 0, len(rows))
	for _, row := range rows {
		items = append(items, map[string]any{
			"id":        row.ID,
			"name":      row.Name,
			"createdAt": row.CreatedAt,
			"updatedAt": row.UpdatedAt,
		})
	}
	httpx.JSON(w, http.StatusOK, items)
}

func (s *Server) uiSnapshots(w http.ResponseWriter, r *http.Request) {
	namespace, ok := s.namespace(w, r.PathValue("name"))
	if !ok {
		return
	}
	limit := uiPageSize
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 100 {
			httpx.Fail(w, http.StatusUnprocessableEntity, "invalid_limit", "limit must be between 1 and 100")
			return
		}
		limit = parsed
	}
	rows, err := s.app.DB.Snapshots(namespace.ID, limit)
	if err != nil {
		s.logger.Error("list snapshots", "error", err)
		httpx.Fail(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	items := make([]snapshotMetaJSON, 0, len(rows))
	for _, row := range rows {
		items = append(items, snapshotMetaJSON{Seq: row.Seq, CreatedAt: row.CreatedAt, TotalBytes: row.TotalBytes})
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"namespace": namespace.Name, "snapshots": items})
}

func (s *Server) uiSnapshot(w http.ResponseWriter, r *http.Request) {
	namespace, ok := s.namespace(w, r.PathValue("name"))
	if !ok {
		return
	}
	seq, err := strconv.ParseInt(r.PathValue("seq"), 10, 64)
	if err != nil || seq < 1 {
		httpx.Fail(w, http.StatusNotFound, "snapshot not found", "snapshot not found")
		return
	}
	snapshot, err := s.app.DB.Snapshot(namespace.ID, seq)
	if err != nil {
		s.logger.Error("read snapshot", "error", err)
		httpx.Fail(w, http.StatusInternalServerError, "internal", "internal error")
		return
	}
	if snapshot == nil {
		httpx.Fail(w, http.StatusNotFound, "snapshot not found", "snapshot not found")
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, "{\"namespace\":%q,\"seq\":%d,\"createdAt\":%d,\"totalBytes\":%d,\"manifest\":",
		namespace.Name, snapshot.Seq, snapshot.CreatedAt, snapshot.TotalBytes)
	_, _ = io.WriteString(w, snapshot.Manifest)
	_, _ = io.WriteString(w, "}\n")
}

func (s *Server) uiBlob(w http.ResponseWriter, r *http.Request) {
	// digest 参与拼接存储路径，先校验再落盘，避免 ../ 之类的路径穿越
	digest := r.PathValue("digest")
	if !core.IsHash(digest) {
		httpx.Fail(w, http.StatusNotFound, "blob not found", "blob not found")
		return
	}
	path := s.app.Blobs.Path(digest)
	if _, err := os.Stat(path); err != nil {
		httpx.Fail(w, http.StatusNotFound, "blob not found", "blob not found")
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

func (s *Server) namespace(w http.ResponseWriter, name string) (namespace, bool) {
	row, err := s.app.DB.NamespaceByName(name)
	if err != nil {
		s.logger.Error("read namespace", "error", err)
		httpx.Fail(w, http.StatusInternalServerError, "internal", "internal error")
		return namespace{}, false
	}
	if row == nil {
		httpx.Fail(w, http.StatusNotFound, "namespace not found", "namespace not found")
		return namespace{}, false
	}
	return namespace{ID: row.ID, Name: row.Name}, true
}

type namespace struct {
	ID   int64
	Name string
}
