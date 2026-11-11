package core

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
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
	Host       string
	Port       int
	DataDir    string
	ReadyToken string
	MaxAPI     int
	MaxStatic  int
	Namespaces []Namespace
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

	path := envOr("CONFIG_FILE", filepath.Join(cfg.DataDir, "config.yaml"))
	namespaces, err := loadNamespaces(path)
	if err != nil {
		return Config{}, err
	}
	cfg.Namespaces = namespaces
	return cfg, nil
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
		if strings.TrimSpace(ns.Name) == "" {
			return nil, fmt.Errorf("namespace name must not be empty")
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
	if strings.Contains(body, ":") {
		return "", "", fmt.Errorf("token body must not contain ':'")
	}
	if perm != "r" && perm != "rw" {
		return "", "", fmt.Errorf("token permission must be r or rw, got %q", perm)
	}
	return body, perm, nil
}

func envOr(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok && value != "" {
		return value
	}
	return fallback
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
