package api

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"sync-center/app"
	"sync-center/core"
)

const (
	testAuthToken = "panel_token_for_test"
	testGCToken   = "gc_token_for_test"
)

func testServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	hash, err := core.HashSecret(testAuthToken)
	if err != nil {
		t.Fatalf("hash secret: %v", err)
	}
	dataDir := t.TempDir()
	application, err := app.New(core.Config{
		DataDir:     dataDir,
		GCTokenHash: core.Digest(testGCToken),
		AuthHash:    hash,
		SessionKey:  []byte("session_signing_key_for_tests_0123456789"),
		MaxAPI:      8,
		MaxStatic:   4,
		LeaseTTL:    time.Minute,
		Namespaces:  []core.Namespace{{Name: "default", Tokens: []string{"sync_default_rw_2026a:rw"}}},
	})
	if err != nil {
		t.Fatalf("new app: %v", err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server := httptest.NewServer(New(application, fstest.MapFS{}, logger).Handler())
	t.Cleanup(server.Close)
	return server, dataDir
}

// 回归：/api/auth 只能取一次 authGate，并发登录不能等到闸门超时。
func TestConcurrentLoginsAllSucceed(t *testing.T) {
	server, _ := testServer(t)
	const logins = 3
	var wg sync.WaitGroup
	codes := make([]int, logins)
	elapsed := make([]time.Duration, logins)
	for i := range logins {
		wg.Add(1)
		go func() {
			defer wg.Done()
			started := time.Now()
			response, err := http.Post(server.URL+"/api/auth", "application/json",
				strings.NewReader("{\"token\":\""+testAuthToken+"\"}"))
			if err != nil {
				t.Errorf("login %d: %v", i, err)
				return
			}
			response.Body.Close()
			codes[i] = response.StatusCode
			elapsed[i] = time.Since(started)
		}()
	}
	wg.Wait()
	for i := range logins {
		if codes[i] != http.StatusOK {
			t.Errorf("login %d = %d, want 200", i, codes[i])
		}
		if elapsed[i] >= gateWait {
			t.Errorf("login %d took %v, want well below the %v gate timeout", i, elapsed[i], gateWait)
		}
	}
}

// 回归：损坏快照让回收停下时，HTTP 要给出可操作的文件名，而不是笼统的 internal。
func TestCollectGarbageReportsDamagedSnapshot(t *testing.T) {
	server, dataDir := testServer(t)
	broken := filepath.Join(dataDir, "namespaces", "default", "00000001.jsonl")
	if err := os.WriteFile(broken, []byte("garbage\n"), 0o600); err != nil {
		t.Fatalf("write damaged snapshot: %v", err)
	}

	request, err := http.NewRequest(http.MethodPost, server.URL+"/admin/api/gc", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	request.Header.Set("Authorization", "Bearer "+testGCToken)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatalf("collect garbage: %v", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if response.StatusCode != http.StatusConflict {
		t.Fatalf("collect garbage = %d %s, want 409", response.StatusCode, body)
	}
	if !strings.Contains(string(body), "00000001.jsonl") {
		t.Fatalf("response %s does not name the damaged snapshot", body)
	}
}
