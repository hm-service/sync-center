package app

import (
	"fmt"
	"os"
	"slices"
	"sort"
	"sync"

	"sync-center/core"
)

// Data 是数据目录上的存储，服务端与 gc 子命令共用。
type Data struct {
	dir string
	// gcMu 保证同一时间只有一次清理；Guard 让清理与请求互斥（清理持写锁，请求持读锁）。
	gcMu      sync.Mutex
	Guard     sync.RWMutex
	Snapshots *core.SnapshotStore
	Blobs     *core.BlobStore
}

func OpenData(dataDir string) (*Data, error) {
	info, err := os.Stat(dataDir)
	if err != nil {
		return nil, fmt.Errorf("data dir: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("data dir is not a directory: %s", dataDir)
	}

	snapshots, err := core.NewSnapshotStore(dataDir)
	if err != nil {
		return nil, fmt.Errorf("open snapshot store: %w", err)
	}
	blobs, err := core.NewBlobStore(dataDir, core.MaxBlobSize)
	if err != nil {
		return nil, fmt.Errorf("open blob store: %w", err)
	}
	return &Data{dir: dataDir, Snapshots: snapshots, Blobs: blobs}, nil
}

type App struct {
	Config core.Config
	*Data
	Tokens     *core.TokenIndex
	Leases     *LeaseStore
	Sessions   *core.Sessions
	Namespaces []string
}

// New 打开数据目录，并为配置里的每个 namespace 建好目录。
func New(config core.Config) (*App, error) {
	data, err := OpenData(config.DataDir)
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(config.DataDir, 0o700); err != nil {
		return nil, fmt.Errorf("tighten data dir: %w", err)
	}

	names := make([]string, 0, len(config.Namespaces))
	for _, namespace := range config.Namespaces {
		if err := data.Snapshots.Ensure(namespace.Name); err != nil {
			return nil, fmt.Errorf("create namespace %q: %w", namespace.Name, err)
		}
		names = append(names, namespace.Name)
	}
	sort.Strings(names)

	tokens, err := core.NewTokenIndex(config.Namespaces)
	if err != nil {
		return nil, err
	}

	return &App{
		Config:     config,
		Data:       data,
		Tokens:     tokens,
		Leases:     NewLeaseStore(config.LeaseTTL),
		Sessions:   core.NewSessions(config.SessionKey),
		Namespaces: names,
	}, nil
}

func (a *App) HasNamespace(name string) bool {
	return slices.Contains(a.Namespaces, name)
}
