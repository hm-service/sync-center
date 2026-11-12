package core

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSplitToken(t *testing.T) {
	cases := []struct {
		name  string
		entry string
		ok    bool
	}{
		{"valid rw", "sync_default_rw_2026a:rw", true},
		{"valid r", "sync_default_ro_2026a:r", true},
		{"too short", "TOKENRW:rw", false},
		{"bad permission", "sync_default_rw_2026a:rwx", false},
		{"missing permission", "sync_default_rw_2026a", false},
		{"contains colon", "sync_default:rw_2026a:rw", false},
		{"contains hyphen", "sync-default-rw-2026a:rw", false},
		{"empty", ":rw", false},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			body, _, err := SplitToken(item.entry)
			if item.ok && err != nil {
				t.Fatalf("expected valid, got %v", err)
			}
			if !item.ok && err == nil {
				t.Fatalf("expected invalid, got body %q", body)
			}
		})
	}
}

func TestNamespacePattern(t *testing.T) {
	cases := []struct {
		name  string
		value string
		ok    bool
	}{
		{"plain", "default", true},
		{"hyphen", "my-work", true},
		{"digits", "2026", true},
		{"slash", "a/b", false},
		{"dot", "a..b", false},
		{"space", "a b", false},
		{"underscore", "a_b", false},
		{"empty", "", false},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			if got := namespacePattern.MatchString(item.value); got != item.ok {
				t.Fatalf("namespacePattern(%q) = %v, want %v", item.value, got, item.ok)
			}
		})
	}
}
func TestTokenIndexRejectsDuplicates(t *testing.T) {
	cases := []struct {
		name       string
		namespaces []Namespace
		ok         bool
	}{
		{
			name: "distinct tokens",
			namespaces: []Namespace{
				{Name: "default", Tokens: []string{"sync_default_rw_2026a:rw", "sync_default_ro_2026a:r"}},
			},
			ok: true,
		},
		{
			name: "same token twice in one namespace",
			namespaces: []Namespace{
				{Name: "default", Tokens: []string{"sync_default_rw_2026a:rw", "sync_default_rw_2026a:r"}},
			},
			ok: false,
		},
		{
			name: "same token in two namespaces",
			namespaces: []Namespace{
				{Name: "default", Tokens: []string{"sync_default_rw_2026a:rw"}},
				{Name: "work", Tokens: []string{"sync_default_rw_2026a:r"}},
			},
			ok: false,
		},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			_, err := NewTokenIndex(item.namespaces)
			if item.ok && err != nil {
				t.Fatalf("expected valid, got %v", err)
			}
			if !item.ok && err == nil {
				t.Fatal("expected an error for a duplicate token")
			}
		})
	}
}

func TestLoadConfigGCToken(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	content := "namespaces:\n  - name: default\n    tokens:\n      - \"sync_default_rw_2026a:rw\"\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CONFIG_FILE", path)
	t.Setenv("DATA_DIR", dir)
	t.Setenv("AUTH_TOKEN", "sync_center_panel")
	t.Setenv("SESSION_SIGNING_KEY", "session_signing_key_7f3a9c2e5b8d1f4a6c0e9b3d7a2f5c8e")

	cases := []struct {
		name    string
		token   string
		enabled bool
		ok      bool
	}{
		{"empty disables the endpoint", "", false, true},
		{"valid token", "gc_token_2026_ops", true, true},
		{"too short", "gc_token", false, false},
		{"bad character", "gc-token-2026-ops", false, false},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			t.Setenv("GC_TOKEN", item.token)
			cfg, err := LoadConfig()
			if !item.ok {
				if err == nil {
					t.Fatal("expected an error for an invalid GC_TOKEN")
				}
				return
			}
			if err != nil {
				t.Fatalf("expected a valid config, got %v", err)
			}
			if cfg.GCEnabled() != item.enabled {
				t.Fatalf("GCEnabled() = %v, want %v", cfg.GCEnabled(), item.enabled)
			}
			if item.enabled && cfg.GCTokenHash != Digest(item.token) {
				t.Fatal("configured digest does not match the token")
			}
		})
	}
}
