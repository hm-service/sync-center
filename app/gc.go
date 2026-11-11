package app

import (
	"time"

	"sync-center/core"
)

const GarbageGrace = time.Hour

// CollectGarbage 汇总所有存活快照引用的 blob，再让存储删掉其余旧文件。
func (a *App) CollectGarbage(grace time.Duration) (int, error) {
	referenced := make(map[string]bool)
	err := a.Snapshots.EachManifest(func(manifest core.Manifest) error {
		for _, entry := range manifest.Files {
			referenced[entry.Hash] = true
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return a.Blobs.Collect(referenced, time.Now().Add(-grace))
}
