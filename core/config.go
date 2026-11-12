package core

import (
	"crypto/sha256"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// namespace 名会成为目录名，只允许字母、数字与横线；token 沿用面板口令的字符集。
var (
	namespacePattern = regexp.MustCompile(`^[A-Za-z0-9-]{1,63}$`)
	tokenPattern     = regexp.MustCompile(`^[A-Za-z0-9_]{16,128}$`)
)

const (
	defaultMaxAPI    = 128
	defaultMaxStatic = 64
)

type Namespace struct {
	Name   string   `yaml:"name"`
	Tokens []string `yaml:"tokens"`
}

type Config struct {
	Host           string
	Port           int
	DataDir        string
	ReadyToken     string
	GCTokenHash    [sha256.Size]byte
	AuthHash       string
	SessionKey     []byte
	MaxAPI         int
	MaxStatic      int
	LeaseTTL       time.Duration
	GCInterval     time.Duration
	TrustedProxies []netip.Prefix
	Namespaces     []Namespace
}

// LoadConfig 读取环境变量与 namespace 配置文件；任何不合法都直接返回错误。
func LoadConfig() (Config, error) {
	cfg := Config{
		Host:       envOr("HOST", "127.0.0.1"),
		DataDir:    envOr("DATA_DIR", "/data"),
		ReadyToken: os.Getenv("READYZ_TOKEN"),
	}
	port, err := intEnv("PORT", 8080, 1, 65535)
	if err != nil {
		return Config{}, err
	}
	cfg.Port = port
	if cfg.MaxAPI, err = intEnv("MAX_API_CONCURRENCY", defaultMaxAPI, 1, 1<<20); err != nil {
		return Config{}, err
	}
	if cfg.MaxStatic, err = intEnv("MAX_STATIC_CONCURRENCY", defaultMaxStatic, 1, 1<<20); err != nil {
		return Config{}, err
	}
	leaseSeconds, err := intEnv("LEASE_TTL", 60, 1, 86400)
	if err != nil {
		return Config{}, err
	}
	cfg.LeaseTTL = time.Duration(leaseSeconds) * time.Second
	gcSeconds, err := intEnv("GC_INTERVAL", 86400, 0, 604800)
	if err != nil {
		return Config{}, err
	}
	cfg.GCInterval = time.Duration(gcSeconds) * time.Second
	if cfg.TrustedProxies, err = prefixEnv("TRUSTED_PROXIES", "127.0.0.1/32,::1/128"); err != nil {
		return Config{}, err
	}

	authToken, err := secretEnv("AUTH_TOKEN", 16, 128)
	if err != nil {
		return Config{}, err
	}
	if cfg.AuthHash, err = HashSecret(authToken); err != nil {
		return Config{}, err
	}
	signingKey, err := secretEnv("SESSION_SIGNING_KEY", 32, 128)
	if err != nil {
		return Config{}, err
	}
	cfg.SessionKey = []byte(signingKey)

	// GC_TOKEN 可选：留空表示不开放按需回收，只保留 GC_INTERVAL 的定时回收
	gcToken, err := optionalSecretEnv("GC_TOKEN", 16, 128)
	if err != nil {
		return Config{}, err
	}
	if gcToken != "" {
		cfg.GCTokenHash = Digest(gcToken)
	}

	path := envOr("CONFIG_FILE", filepath.Join(cfg.DataDir, "config.yaml"))
	namespaces, err := loadNamespaces(path)
	if err != nil {
		return Config{}, err
	}
	cfg.Namespaces = namespaces
	return cfg, nil
}

// GCEnabled 报告 GC_TOKEN 是否配置；未配置时 /admin/api/gc 不注册。
func (c Config) GCEnabled() bool {
	return c.GCTokenHash != [sha256.Size]byte{}
}

func loadNamespaces(path string) ([]Namespace, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config file: %w", err)
	}
	var file struct {
		Namespaces []Namespace `yaml:"namespaces"`
	}
	if err := yaml.Unmarshal(raw, &file); err != nil {
		return nil, fmt.Errorf("parse config file: %w", err)
	}
	if len(file.Namespaces) == 0 {
		return nil, fmt.Errorf("config file must declare at least one namespace")
	}

	seen := make(map[string]bool, len(file.Namespaces))
	for i := range file.Namespaces {
		ns := &file.Namespaces[i]
		if !namespacePattern.MatchString(ns.Name) {
			return nil, fmt.Errorf("namespace %q must be 1-63 characters of letters, digits and hyphens", ns.Name)
		}
		if seen[ns.Name] {
			return nil, fmt.Errorf("duplicate namespace %q", ns.Name)
		}
		if len(ns.Tokens) == 0 {
			return nil, fmt.Errorf("namespace %q must declare at least one token", ns.Name)
		}
		for _, entry := range ns.Tokens {
			if _, _, err := SplitToken(entry); err != nil {
				return nil, fmt.Errorf("namespace %q: %w", ns.Name, err)
			}
		}
		seen[ns.Name] = true
	}
	return file.Namespaces, nil
}

// SplitToken 把 "<token>:<r|rw>" 拆成 token 主体与权限。
func SplitToken(entry string) (string, string, error) {
	i := strings.LastIndex(entry, ":")
	if i <= 0 || i == len(entry)-1 {
		return "", "", fmt.Errorf("invalid token entry %q", entry)
	}
	body, perm := entry[:i], entry[i+1:]
	if !tokenPattern.MatchString(body) {
		return "", "", fmt.Errorf("token must be 16-128 characters of letters, digits and underscores")
	}
	if perm != "r" && perm != "rw" {
		return "", "", fmt.Errorf("token permission must be r or rw, got %q", perm)
	}
	return body, perm, nil
}

// DataDirFromEnv 只读取数据目录，供不校验服务端密钥的 gc 子命令使用。
func DataDirFromEnv() string {
	return envOr("DATA_DIR", "/data")
}

func envOr(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok && value != "" {
		return value
	}
	return fallback
}

func secretEnv(key string, minLen, maxLen int) (string, error) {
	value := os.Getenv(key)
	if err := checkSecret(key, value, minLen, maxLen); err != nil {
		return "", err
	}
	return value, nil
}

// optionalSecretEnv 允许留空，留空表示对应能力不启用；非空时格式要求与 secretEnv 相同。
func optionalSecretEnv(key string, minLen, maxLen int) (string, error) {
	value := os.Getenv(key)
	if value == "" {
		return "", nil
	}
	if err := checkSecret(key, value, minLen, maxLen); err != nil {
		return "", err
	}
	return value, nil
}

func checkSecret(key, value string, minLen, maxLen int) error {
	if len(value) < minLen || len(value) > maxLen {
		return fmt.Errorf("%s must be %d-%d characters long", key, minLen, maxLen)
	}
	for _, r := range value {
		valid := r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_'
		if !valid {
			return fmt.Errorf("%s may only contain letters, digits and underscores", key)
		}
	}
	return nil
}

func prefixEnv(key, fallback string) ([]netip.Prefix, error) {
	raw, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(raw) == "" {
		raw = fallback
	}
	var prefixes []netip.Prefix
	for _, item := range strings.Split(raw, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		prefix, err := netip.ParsePrefix(item)
		if err != nil {
			return nil, fmt.Errorf("%s must be comma-separated CIDRs, got %q", key, item)
		}
		prefixes = append(prefixes, prefix.Masked())
	}
	return prefixes, nil
}

func intEnv(key string, fallback, low, high int) (int, error) {
	value, ok := os.LookupEnv(key)
	if !ok || value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("%s must be an integer, got %q", key, value)
	}
	if parsed < low || parsed > high {
		return 0, fmt.Errorf("%s out of range [%d,%d]: %d", key, low, high, parsed)
	}
	return parsed, nil
}
