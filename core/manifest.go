package core

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

const (
	MaxFiles    = 100_000
	MaxPathLen  = 1024
	defaultMode = 0o644
)

var hex64 = regexp.MustCompile("^[0-9a-f]{64}$")

type Entry struct {
	Path  string `json:"path"`
	Hash  string `json:"hash"`
	Size  int64  `json:"size"`
	Mode  int    `json:"mode"`
	Mtime int64  `json:"mtime"`
}

type Manifest struct {
	Files []Entry `json:"files"`
}

// Normalize 校验 manifest 并返回规范化后的 JSON 文本与文件总字节数。
// 文本按 path 的字节序排序、不含空白，服务端只存这一份。
func Normalize(raw json.RawMessage) (string, int64, error) {
	var manifest Manifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return "", 0, fmt.Errorf("manifest must be an object with a files array")
	}
	if len(manifest.Files) > MaxFiles {
		return "", 0, fmt.Errorf("manifest has too many files")
	}

	seen := make(map[string]bool, len(manifest.Files))
	total := int64(0)
	for i := range manifest.Files {
		entry := &manifest.Files[i]
		if err := validatePath(entry.Path); err != nil {
			return "", 0, err
		}
		if !hex64.MatchString(entry.Hash) {
			return "", 0, fmt.Errorf("invalid hash for %q", entry.Path)
		}
		if entry.Size < 0 {
			return "", 0, fmt.Errorf("invalid size for %q", entry.Path)
		}
		if entry.Mode == 0 {
			entry.Mode = defaultMode
		}
		if entry.Mode < 0 || entry.Mode > 0o777 {
			return "", 0, fmt.Errorf("invalid mode for %q", entry.Path)
		}
		if entry.Mtime < 0 {
			return "", 0, fmt.Errorf("invalid mtime for %q", entry.Path)
		}
		if seen[entry.Path] {
			return "", 0, fmt.Errorf("duplicate path %q", entry.Path)
		}
		seen[entry.Path] = true
		total += entry.Size
	}

	sort.Slice(manifest.Files, func(i, j int) bool {
		return manifest.Files[i].Path < manifest.Files[j].Path
	})

	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(manifest); err != nil {
		return "", 0, err
	}
	return strings.TrimSuffix(buf.String(), "\n"), total, nil
}

// Parse 把存储的 manifest 文本解回结构，供 WebUI 与校验使用。
func Parse(text string) (Manifest, error) {
	var manifest Manifest
	err := json.Unmarshal([]byte(text), &manifest)
	return manifest, err
}

// IsHash 判断字符串是否是可用的 blob 摘要。
func IsHash(value string) bool {
	return hex64.MatchString(value)
}

func validatePath(path string) error {
	if path == "" || len(path) > MaxPathLen {
		return fmt.Errorf("invalid path")
	}
	if strings.Contains(path, "\\") || strings.HasPrefix(path, "/") || strings.HasSuffix(path, "/") {
		return fmt.Errorf("invalid path %q", path)
	}
	for _, r := range path {
		if r < 0x20 {
			return fmt.Errorf("invalid path %q", path)
		}
	}
	for _, segment := range strings.Split(path, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return fmt.Errorf("invalid path %q", path)
		}
	}
	return nil
}
