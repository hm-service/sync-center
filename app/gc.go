package app

import (
	"time"

	"sync-center/core"
)

const GarbageGrace = time.Hour

// CollectGarbage 汇总所有存活快照引用的 blob，再让存储删掉其余旧文件。
func (a *App) CollectGarbage(grace time.Duration) (int, error) {
	manifests, err := a.DB.AllManifests()
	if err != nil {
		return 0, err
	}
	referenced := make(map[string]bool)
	for _, text := range manifests {
		manifest, err := core.Parse(text)
		if err != nil {
			return 0, err
		}
		for _, entry := range manifest.Files {
			referenced[entry.Hash] = true
		}
	}
	return a.Blobs.Collect(referenced, time.Now().Add(-grace))
}
