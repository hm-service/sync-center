package app

import (
	"fmt"
	"os"
	"path/filepath"

	"sync-center/core"
	"sync-center/db"
)

type App struct {
	Config       core.Config
	DB           *db.DB
	Blobs        *core.BlobStore
	Tokens       *core.TokenIndex
	Leases       *LeaseStore
	Sessions     *core.Sessions
	NamespaceIDs map[string]int64
}

// New 打开数据目录、数据库与 blob 存储，并把配置里的 namespace 同步到库中。
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

	dbPath := filepath.Join(config.DataDir, "sync.db")
	database, err := db.Open(dbPath)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	if err := os.Chmod(dbPath, 0o600); err != nil {
		database.Close()
		return nil, fmt.Errorf("tighten database: %w", err)
	}

	names := make([]string, 0, len(config.Namespaces))
	for _, namespace := range config.Namespaces {
		names = append(names, namespace.Name)
	}
	if err := database.SyncNamespaces(names); err != nil {
		database.Close()
		return nil, fmt.Errorf("sync namespaces: %w", err)
	}

	tokens, err := core.NewTokenIndex(config.Namespaces)
	if err != nil {
		database.Close()
		return nil, err
	}

	blobs, err := core.NewBlobStore(config.DataDir, core.MaxBlobSize)
	if err != nil {
		database.Close()
		return nil, fmt.Errorf("open blob store: %w", err)
	}

	ids := make(map[string]int64, len(names))
	for _, name := range names {
		row, err := database.NamespaceByName(name)
		if err != nil {
			database.Close()
			return nil, fmt.Errorf("load namespace: %w", err)
		}
		if row == nil {
			database.Close()
			return nil, fmt.Errorf("namespace %q missing after sync", name)
		}
		ids[name] = row.ID
	}

	return &App{
		Config:       config,
		DB:           database,
		Blobs:        blobs,
		Tokens:       tokens,
		Leases:       NewLeaseStore(leaseTTL),
		Sessions:     core.NewSessions(config.SessionKey),
		NamespaceIDs: ids,
	}, nil
}

func (a *App) Close() error {
	return a.DB.Close()
}
