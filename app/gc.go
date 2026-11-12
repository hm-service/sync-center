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

// CollectGarbage 阻塞等待清理权，供 gc 子命令使用。
// 整个过程持写锁，请求会排队，因此引用集不会在扫描与删除之间变化。
func (d *Data) CollectGarbage(grace time.Duration) (int, error) {
	d.gcMu.Lock()
	defer d.gcMu.Unlock()
	d.Guard.Lock()
	defer d.Guard.Unlock()
	return d.collectGarbage(grace)
}

// TryCollectGarbage 已有清理在跑时返回 ran=false；否则等当前请求结束再开始。
func (d *Data) TryCollectGarbage(grace time.Duration) (removed int, ran bool, err error) {
	if !d.gcMu.TryLock() {
		return 0, false, nil
	}
	defer d.gcMu.Unlock()
	d.Guard.Lock()
	defer d.Guard.Unlock()
	removed, err = d.collectGarbage(grace)
	return removed, true, err
}

func (d *Data) collectGarbage(grace time.Duration) (int, error) {
	// 旧布局与 Python 原型的快照都在 sync.db 里，看不见这些引用就删 blob 是不可恢复的
	if _, err := os.Stat(filepath.Join(d.dir, "sync.db")); err == nil {
		return 0, fmt.Errorf("found sync.db in %s: it belongs to another layout, refusing to delete blobs", d.dir)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return 0, err
	}

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
