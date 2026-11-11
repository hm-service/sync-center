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
		s.fail(w, r, http.StatusInternalServerError, "internal", "error.internal")
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
	namespace, ok := s.namespace(w, r, r.PathValue("name"))
	if !ok {
		return
	}
	limit := uiPageSize
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 100 {
			s.fail(w, r, http.StatusUnprocessableEntity, "invalid_limit", "error.invalid-limit")
			return
		}
		limit = parsed
	}
	rows, err := s.app.DB.Snapshots(namespace.ID, limit)
	if err != nil {
		s.logger.Error("list snapshots", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "internal", "error.internal")
		return
	}
	items := make([]snapshotMetaJSON, 0, len(rows))
	for _, row := range rows {
		items = append(items, snapshotMetaJSON{Seq: row.Seq, CreatedAt: row.CreatedAt, TotalBytes: row.TotalBytes})
	}
	httpx.JSON(w, http.StatusOK, map[string]any{"namespace": namespace.Name, "snapshots": items})
}

func (s *Server) uiSnapshot(w http.ResponseWriter, r *http.Request) {
	namespace, ok := s.namespace(w, r, r.PathValue("name"))
	if !ok {
		return
	}
	seq, err := strconv.ParseInt(r.PathValue("seq"), 10, 64)
	if err != nil || seq < 1 {
		s.fail(w, r, http.StatusNotFound, "snapshot_not_found", "error.snapshot-not-found")
		return
	}
	snapshot, err := s.app.DB.Snapshot(namespace.ID, seq)
	if err != nil {
		s.logger.Error("read snapshot", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "internal", "error.internal")
		return
	}
	if snapshot == nil {
		s.fail(w, r, http.StatusNotFound, "snapshot_not_found", "error.snapshot-not-found")
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

func (s *Server) namespace(w http.ResponseWriter, r *http.Request, name string) (namespace, bool) {
	row, err := s.app.DB.NamespaceByName(name)
	if err != nil {
		s.logger.Error("read namespace", "error", err)
		s.fail(w, r, http.StatusInternalServerError, "internal", "error.internal")
		return namespace{}, false
	}
	if row == nil {
		s.fail(w, r, http.StatusNotFound, "namespace_not_found", "error.namespace-not-found")
		return namespace{}, false
	}
	return namespace{ID: row.ID, Name: row.Name}, true
}

type namespace struct {
	ID   int64
	Name string
}
