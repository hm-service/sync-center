package app

import (
	"fmt"
	"os"
	"sort"

	"sync-center/core"
)

type App struct {
	Config     core.Config
	Snapshots  *core.SnapshotStore
	Blobs      *core.BlobStore
	Tokens     *core.TokenIndex
	Leases     *LeaseStore
	Sessions   *core.Sessions
	Namespaces []string
}

// New 打开数据目录、快照与 blob 存储，并为配置里的每个 namespace 建好目录。
func New(config core.Config) (*App, error) {
	info, err := os.Stat(config.DataDir)
	if err != nil {
		return nil, fmt.Errorf("data dir: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("data dir is not a directory: %s", config.DataDir)
	}
	if err := os.Chmod(config.DataDir, 0o700); err != nil {
		return nil, fmt.Errorf("tighten data dir: %w", err)
	}

	snapshots, err := core.NewSnapshotStore(config.DataDir)
	if err != nil {
		return nil, fmt.Errorf("open snapshot store: %w", err)
	}

	names := make([]string, 0, len(config.Namespaces))
	for _, namespace := range config.Namespaces {
		if err := snapshots.Ensure(namespace.Name); err != nil {
			return nil, fmt.Errorf("create namespace %q: %w", namespace.Name, err)
		}
		names = append(names, namespace.Name)
	}
	sort.Strings(names)

	tokens, err := core.NewTokenIndex(config.Namespaces)
	if err != nil {
		return nil, err
	}

	blobs, err := core.NewBlobStore(config.DataDir, core.MaxBlobSize)
	if err != nil {
		return nil, fmt.Errorf("open blob store: %w", err)
	}

	return &App{
		Config:     config,
		Snapshots:  snapshots,
		Blobs:      blobs,
		Tokens:     tokens,
		Leases:     NewLeaseStore(config.LeaseTTL),
		Sessions:   core.NewSessions(config.SessionKey),
		Namespaces: names,
	}, nil
}

func (a *App) HasNamespace(name string) bool {
	for _, candidate := range a.Namespaces {
		if candidate == name {
			return true
		}
	}
	return false
}
