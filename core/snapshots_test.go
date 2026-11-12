package core

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func testStore(t *testing.T) *SnapshotStore {
	t.Helper()
	store, err := NewSnapshotStore(t.TempDir())
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	if err := store.Ensure("default"); err != nil {
		t.Fatalf("ensure namespace: %v", err)
	}
	return store
}

func testManifest(paths ...string) Manifest {
	files := make([]Entry, 0, len(paths))
	for i, path := range paths {
		files = append(files, Entry{
			Path:  path,
			Hash:  fmt.Sprintf("%064x", i+1),
			Size:  int64(i + 1),
			Mode:  0o644,
			Mtime: 1700000000000,
		})
	}
	return Manifest{Files: files}
}

//go:fix inline
func seqPtr(value int64) *int64 { return new(value) }

func TestCommitCreatesVersions(t *testing.T) {
	store := testStore(t)

	seq, err := store.Commit("default", nil, testManifest("a.txt"), 1, nil)
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if seq != 1 {
		t.Fatalf("first seq = %d, want 1", seq)
	}

	seq, err = store.Commit("default", seqPtr(1), testManifest("a.txt", "b.txt"), 3, nil)
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if seq != 2 {
		t.Fatalf("second seq = %d, want 2", seq)
	}

	metas, _, err := store.List("default", 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(metas) != 2 || metas[0].Seq != 2 || metas[1].Seq != 1 {
		t.Fatalf("list = %+v, want seq 2 then 1", metas)
	}
	if metas[0].TotalBytes != 3 {
		t.Fatalf("totalBytes = %d, want 3", metas[0].TotalBytes)
	}
}

func TestCommitRejectsStaleParent(t *testing.T) {
	store := testStore(t)
	if _, err := store.Commit("default", nil, testManifest("a.txt"), 1, nil); err != nil {
		t.Fatalf("commit: %v", err)
	}
	_, err := store.Commit("default", nil, testManifest("b.txt"), 1, nil)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("err = %v, want ErrConflict", err)
	}
}

func TestCommitSkipsUnchangedManifest(t *testing.T) {
	store := testStore(t)
	manifest := testManifest("a.txt", "b.txt")
	seq, err := store.Commit("default", nil, manifest, 3, nil)
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	again, err := store.Commit("default", new(seq), manifest, 3, nil)
	if err != nil {
		t.Fatalf("commit: %v", err)
	}
	if again != seq {
		t.Fatalf("seq = %d, want %d", again, seq)
	}
	entries, err := os.ReadDir(filepath.Join(store.root, "default"))
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("snapshot files = %d, want 1", len(entries))
	}
}

func TestCommitPrunesOldVersions(t *testing.T) {
	store := testStore(t)
	var parent *int64
	for i := range KeepVersions + 3 {
		seq, err := store.Commit("default", parent, testManifest(fmt.Sprintf("f%d.txt", i)), 1, nil)
		if err != nil {
			t.Fatalf("commit %d: %v", i, err)
		}
		parent = new(seq)
	}
	metas, _, err := store.List("default", 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(metas) != KeepVersions {
		t.Fatalf("versions = %d, want %d", len(metas), KeepVersions)
	}
	if metas[len(metas)-1].Seq != 4 {
		t.Fatalf("oldest seq = %d, want 4", metas[len(metas)-1].Seq)
	}
}

func TestWriteJSONStreamsManifest(t *testing.T) {
	store := testStore(t)
	manifest := testManifest("a.txt", "b/c.txt")
	seq, err := store.Commit("default", nil, manifest, 3, nil)
	if err != nil {
		t.Fatalf("commit: %v", err)
	}

	snapshot, err := store.Open("default", seq)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer snapshot.Close()

	var buffer bytes.Buffer
	if err := snapshot.WriteJSON(&buffer); err != nil {
		t.Fatalf("write json: %v", err)
	}

	var decoded struct {
		Seq        int64    `json:"seq"`
		CreatedAt  int64    `json:"createdAt"`
		TotalBytes int64    `json:"totalBytes"`
		Manifest   Manifest `json:"manifest"`
	}
	if err := json.Unmarshal(buffer.Bytes(), &decoded); err != nil {
		t.Fatalf("decode: %v (%s)", err, buffer.String())
	}
	if decoded.Seq != seq || decoded.TotalBytes != 3 {
		t.Fatalf("decoded = %+v", decoded)
	}
	if len(decoded.Manifest.Files) != 2 || decoded.Manifest.Files[1].Path != "b/c.txt" {
		t.Fatalf("files = %+v", decoded.Manifest.Files)
	}
}

func TestOpenMissingSnapshot(t *testing.T) {
	store := testStore(t)
	snapshot, err := store.Open("default", 7)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if snapshot != nil {
		t.Fatalf("snapshot = %+v, want nil", snapshot)
	}
}

func TestListReportsMismatchedFileName(t *testing.T) {
	store := testStore(t)
	if _, err := store.Commit("default", nil, testManifest("a.txt"), 1, nil); err != nil {
		t.Fatalf("commit: %v", err)
	}
	source := filepath.Join(store.root, "default", "00000001.jsonl")
	if err := os.WriteFile(filepath.Join(store.root, "default", "00000009.jsonl"), mustRead(t, source), 0o600); err != nil {
		t.Fatalf("write copy: %v", err)
	}
	metas, damaged, err := store.List("default", 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(metas) != 1 || metas[0].Seq != 1 {
		t.Fatalf("metas = %+v, want only seq 1", metas)
	}
	if len(damaged) != 1 || damaged[0] != "00000009.jsonl" {
		t.Fatalf("damaged = %+v, want [00000009.jsonl]", damaged)
	}
	if !DamagedAhead(damaged, metas[0].Seq) {
		t.Fatal("DamagedAhead missed a damaged newer snapshot")
	}
	if DamagedAhead(damaged, 9) {
		t.Fatal("DamagedAhead treated an older damaged snapshot as newer")
	}
}

func TestEachManifestRejectsDamagedSnapshot(t *testing.T) {
	store := testStore(t)
	if _, err := store.Commit("default", nil, testManifest("a.txt"), 1, nil); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if err := os.WriteFile(filepath.Join(store.root, "default", "00000002.jsonl"), []byte("not json\n"), 0o600); err != nil {
		t.Fatalf("write damaged: %v", err)
	}
	if err := store.EachManifest(func(Manifest) error { return nil }); err == nil {
		t.Fatal("EachManifest accepted a damaged snapshot")
	}
}

func TestEachManifest(t *testing.T) {
	store := testStore(t)
	if _, err := store.Commit("default", nil, testManifest("a.txt"), 1, nil); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if _, err := store.Commit("default", seqPtr(1), testManifest("a.txt", "b.txt"), 3, nil); err != nil {
		t.Fatalf("commit: %v", err)
	}
	count := 0
	err := store.EachManifest(func(manifest Manifest) error {
		count++
		return nil
	})
	if err != nil {
		t.Fatalf("each manifest: %v", err)
	}
	if count != 2 {
		t.Fatalf("manifests = %d, want 2", count)
	}
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data
}

func TestCommitRunsVerifierBeforeWriting(t *testing.T) {
	store := testStore(t)
	rejected := errors.New("blob is gone")
	if _, err := store.Commit("default", nil, testManifest("a.txt"), 1, func() error { return rejected }); !errors.Is(err, rejected) {
		t.Fatalf("Commit() error = %v, want the verifier error", err)
	}
	metas, _, err := store.List("default", 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(metas) != 0 {
		t.Fatalf("a rejected commit wrote %d snapshots", len(metas))
	}
}

// 校验必须在共享锁内跑完：否则外部 gc 能在"校验通过"与"落盘"之间删掉 blob。
func TestCommitHoldsSharedLockDuringVerification(t *testing.T) {
	store := testStore(t)
	acquired := make(chan struct{})
	verify := func() error {
		go func() {
			lock, err := store.LockAll()
			if err != nil {
				t.Errorf("take exclusive lock: %v", err)
				return
			}
			lock.Close()
			close(acquired)
		}()
		select {
		case <-acquired:
			t.Error("exclusive lock was granted while the verifier was still running")
		case <-time.After(100 * time.Millisecond):
		}
		return nil
	}
	if _, err := store.Commit("default", nil, testManifest("a.txt"), 1, verify); err != nil {
		t.Fatalf("commit: %v", err)
	}
	select {
	case <-acquired:
	case <-time.After(5 * time.Second):
		t.Fatal("exclusive lock was not granted after the commit finished")
	}
}

func TestEachManifestReportsDamagedFile(t *testing.T) {
	store := testStore(t)
	if _, err := store.Commit("default", nil, testManifest("a.txt"), 1, nil); err != nil {
		t.Fatalf("commit: %v", err)
	}
	broken := filepath.Join(store.root, "default", "00000002.jsonl")
	if err := os.WriteFile(broken, []byte("garbage\n"), 0o600); err != nil {
		t.Fatalf("write damaged snapshot: %v", err)
	}
	err := store.EachManifest(func(Manifest) error { return nil })
	var damaged *DamagedError
	if !errors.As(err, &damaged) {
		t.Fatalf("EachManifest() error = %v, want *DamagedError", err)
	}
	if damaged.Namespace != "default" || damaged.File != "00000002.jsonl" {
		t.Fatalf("DamagedError = %+v", damaged)
	}
}
