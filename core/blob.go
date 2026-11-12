package core

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

var (
	ErrBlobMismatch = errors.New("blob digest mismatch")
	ErrBlobTooLarge = errors.New("blob exceeds size limit")
)

const MaxBlobSize = 256 << 20

type BlobStore struct {
	root string
	tmp  string
	max  int64
}

func NewBlobStore(dataDir string, maxSize int64) (*BlobStore, error) {
	store := &BlobStore{
		root: filepath.Join(dataDir, "blobs"),
		tmp:  filepath.Join(dataDir, "tmp"),
		max:  maxSize,
	}
	for _, dir := range []string{store.root, store.tmp} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
	}
	return store, nil
}

// Path 只在 hash 已校验为 64 位小写十六进制后调用。
func (b *BlobStore) Path(hash string) string {
	return filepath.Join(b.root, hash[:2], hash[2:4], hash)
}

func (b *BlobStore) Stat(hash string) (int64, bool) {
	info, err := os.Stat(b.Path(hash))
	if err != nil {
		return 0, false
	}
	return info.Size(), true
}

// Write 边写临时文件边算摘要，校验通过后原子改名。
func (b *BlobStore) Write(hash string, r io.Reader) (int64, error) {
	tmp, err := os.CreateTemp(b.tmp, hash+".*")
	if err != nil {
		return 0, err
	}
	defer os.Remove(tmp.Name())

	hasher := sha256.New()
	size, err := io.Copy(io.MultiWriter(tmp, hasher), io.LimitReader(r, b.max+1))
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return 0, err
	}
	if size > b.max {
		return 0, ErrBlobTooLarge
	}
	if hex.EncodeToString(hasher.Sum(nil)) != hash {
		return 0, ErrBlobMismatch
	}

	target := b.Path(hash)
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return 0, err
	}
	if err := os.Rename(tmp.Name(), target); err != nil {
		return 0, err
	}
	// 目录项也要落盘，否则崩溃后可能只剩一个空名字
	if err := syncDir(filepath.Dir(target)); err != nil {
		return 0, err
	}
	return size, nil
}

// Collect 删除未被任何快照引用、且修改时间早于 cutoff 的 blob 与残留临时文件。
func (b *BlobStore) Collect(referenced map[string]bool, cutoff time.Time) (int, error) {
	removed := 0
	err := filepath.WalkDir(b.root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || referenced[entry.Name()] {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if info.ModTime().After(cutoff) {
			return nil
		}
		if err := os.Remove(path); err != nil {
			return err
		}
		removed++
		return nil
	})
	if err != nil {
		return removed, err
	}

	entries, err := os.ReadDir(b.tmp)
	if err != nil {
		return removed, err
	}
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if !info.IsDir() && !info.ModTime().After(cutoff) {
			_ = os.Remove(filepath.Join(b.tmp, entry.Name()))
		}
	}
	return removed, nil
}
