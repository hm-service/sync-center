package app

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"sync-center/core"
)

const GarbageGrace = time.Hour

// CollectGarbage 汇总所有存活快照引用的 blob，再让存储删掉其余旧文件。
// 整个过程持独占锁，提交会等它结束，因此引用集不会在扫描与删除之间变化。
func (d *Data) CollectGarbage(grace time.Duration) (int, error) {
	// 旧布局与 Python 原型的快照都在 sync.db 里，看不见这些引用就删 blob 是不可恢复的
	if _, err := os.Stat(filepath.Join(d.dir, "sync.db")); err == nil {
		return 0, fmt.Errorf("found sync.db in %s: it belongs to another layout, refusing to delete blobs", d.dir)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return 0, err
	}

	// 清理期间暂停请求，避免引用集在扫描与删除之间变化
	d.Guard.Lock()
	defer d.Guard.Unlock()

	lock, err := d.Snapshots.LockAll()
	if err != nil {
		return 0, err
	}
	defer lock.Close()

	referenced := make(map[string]bool)
	err = d.Snapshots.EachManifest(func(manifest core.Manifest) error {
		for _, entry := range manifest.Files {
			referenced[entry.Hash] = true
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return d.Blobs.Collect(referenced, time.Now().Add(-grace))
}
