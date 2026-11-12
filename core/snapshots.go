package core

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ErrConflict 表示提交携带的 parent 与当前 head 不一致。
var ErrConflict = errors.New("parent does not match head")

// KeepVersions 是每个 namespace 保留的快照数量。
const KeepVersions = 10

// maxLine 限制单行长度，损坏的文件在读取时报错，而不是把内存吃空。
const maxLine = 64 << 10

// SnapshotMeta 是快照文件首行的内容，也是列表接口返回的字段。
type SnapshotMeta struct {
	Seq        int64 `json:"seq"`
	CreatedAt  int64 `json:"createdAt"`
	TotalBytes int64 `json:"totalBytes"`
}

// NamespaceInfo 由快照文件推导，不额外落盘。
type NamespaceInfo struct {
	Name      string
	CreatedAt int64
	UpdatedAt int64
}

// SnapshotStore 每个快照一个 JSONL 文件：首行元数据，其余每行一个文件条目。
// 提交是唯一的写入路径，读取全部流式进行。
type SnapshotStore struct {
	root string
	tmp  string
	lock string
	mu   sync.Mutex
}

func NewSnapshotStore(dataDir string) (*SnapshotStore, error) {
	root := filepath.Join(dataDir, "namespaces")
	tmp := filepath.Join(dataDir, "tmp")
	for _, dir := range []string{root, tmp} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
	}
	return &SnapshotStore{
		root: root,
		tmp:  tmp,
		lock: filepath.Join(dataDir, ".lock"),
	}, nil
}

// LockAll 取独占锁，让垃圾回收期间没有提交在改引用集。
// 只在独立进程里调用：同进程再取共享锁会与自己的独占锁互等。
func (s *SnapshotStore) LockAll() (*FileLock, error) {
	return LockFile(s.lock, true)
}

// Ensure 创建 namespace 目录；namespace 只来自配置，名称在配置层已校验。
func (s *SnapshotStore) Ensure(name string) error {
	return os.MkdirAll(s.dir(name), 0o700)
}

func (s *SnapshotStore) dir(name string) string {
	return filepath.Join(s.root, name)
}

// file 用定宽 seq 命名，目录看起来整齐；排序仍按数值。
func (s *SnapshotStore) file(name string, seq int64) string {
	return filepath.Join(s.dir(name), fmt.Sprintf("%08d.jsonl", seq))
}

// List 按 seq 从新到旧返回可读的元数据；limit <= 0 表示不限制。
// 第二个返回值是无法解析的文件名。并发裁剪导致文件消失不算损坏，直接跳过。
func (s *SnapshotStore) List(name string, limit int) ([]SnapshotMeta, []string, error) {
	dir := s.dir(name)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil, nil
		}
		return nil, nil, err
	}
	metas := make([]SnapshotMeta, 0, len(entries))
	var damaged []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		seq, ok := parseSeq(entry.Name())
		if !ok {
			continue
		}
		meta, err := readMeta(filepath.Join(dir, entry.Name()))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil || meta.Seq != seq {
			damaged = append(damaged, entry.Name())
			continue
		}
		metas = append(metas, meta)
	}
	sort.Slice(metas, func(i, j int) bool { return metas[i].Seq > metas[j].Seq })
	if limit > 0 && len(metas) > limit {
		metas = metas[:limit]
	}
	return metas, damaged, nil
}

// DamagedAhead 报告损坏的文件里是否有比 head 更新的版本。
// 有的话不能把更旧的快照当作 latest 返回，否则客户端会拉回旧状态。
func DamagedAhead(damaged []string, head int64) bool {
	for _, name := range damaged {
		if seq, ok := parseSeq(name); ok && seq > head {
			return true
		}
	}
	return false
}

// Info 返回 namespace 的时间摘要：最早与最近一次快照的时间。
func (s *SnapshotStore) Info(name string) (NamespaceInfo, []string, error) {
	metas, damaged, err := s.List(name, 0)
	if err != nil {
		return NamespaceInfo{}, nil, err
	}
	info := NamespaceInfo{Name: name}
	if len(metas) == 0 {
		// 没有快照时用目录时间，避免空 namespace 显示 1970
		stat, err := os.Stat(s.dir(name))
		if err != nil {
			return NamespaceInfo{}, damaged, err
		}
		info.CreatedAt = stat.ModTime().Unix()
		info.UpdatedAt = info.CreatedAt
		return info, damaged, nil
	}
	info.CreatedAt = metas[len(metas)-1].CreatedAt
	info.UpdatedAt = metas[0].CreatedAt
	return info, damaged, nil
}

// Snapshot 是已定位并打开的快照文件，读取时逐行流式处理。
type Snapshot struct {
	Meta    SnapshotMeta
	file    *os.File
	scanner *bufio.Scanner
}

// Open 打开指定快照；不存在时返回 nil, nil。
func (s *SnapshotStore) Open(name string, seq int64) (*Snapshot, error) {
	path := s.file(name, seq)
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	scanner := newScanner(file)
	if !scanner.Scan() {
		file.Close()
		if err := scanner.Err(); err != nil {
			return nil, fmt.Errorf("%s: %w", filepath.Base(path), err)
		}
		return nil, fmt.Errorf("%s: empty snapshot", filepath.Base(path))
	}
	var meta SnapshotMeta
	if err := json.Unmarshal(scanner.Bytes(), &meta); err != nil {
		file.Close()
		return nil, fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	if meta.Seq != seq {
		file.Close()
		return nil, fmt.Errorf("%s: seq %d does not match file name", filepath.Base(path), meta.Seq)
	}
	return &Snapshot{Meta: meta, file: file, scanner: scanner}, nil
}

func (s *Snapshot) Close() error {
	return s.file.Close()
}

// WriteJSON 把快照写成 API 响应结构，逐行转发，不把 manifest 读进内存。
func (s *Snapshot) WriteJSON(w io.Writer) error {
	if _, err := fmt.Fprintf(w, `{"seq":%d,"createdAt":%d,"totalBytes":%d,"manifest":{"files":[`,
		s.Meta.Seq, s.Meta.CreatedAt, s.Meta.TotalBytes); err != nil {
		return err
	}
	first := true
	for s.scanner.Scan() {
		if !first {
			if _, err := io.WriteString(w, ","); err != nil {
				return err
			}
		}
		first = false
		if _, err := w.Write(s.scanner.Bytes()); err != nil {
			return err
		}
	}
	if err := s.scanner.Err(); err != nil {
		return err
	}
	_, err := io.WriteString(w, "]}}\n")
	return err
}

// Commit 写入新快照。parent 为 nil 表示期望当前 namespace 没有快照。
// 内容与 head 完全一致时不产生新版本。
func (s *SnapshotStore) Commit(name string, parent *int64, manifest Manifest, total int64) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// 共享锁与垃圾回收的独占锁互斥：回收期间不会有人改引用集
	lock, err := LockFile(s.lock, false)
	if err != nil {
		return 0, err
	}
	defer lock.Close()

	metas, _, err := s.List(name, 1)
	if err != nil {
		return 0, err
	}
	var head SnapshotMeta
	if len(metas) > 0 {
		head = metas[0]
	}

	expected := int64(0)
	if parent != nil {
		expected = *parent
	}
	if expected != head.Seq {
		return 0, ErrConflict
	}

	tmp, err := os.CreateTemp(s.tmp, "snapshot-*")
	if err != nil {
		return 0, err
	}
	defer os.Remove(tmp.Name())

	seq := head.Seq + 1
	meta := SnapshotMeta{Seq: seq, CreatedAt: time.Now().Unix(), TotalBytes: total}
	header, err := json.Marshal(meta)
	if err != nil {
		tmp.Close()
		return 0, err
	}
	if _, err := tmp.Write(append(header, '\n')); err != nil {
		tmp.Close()
		return 0, err
	}

	identical := head.Seq > 0
	var headScanner *lineScanner
	if identical {
		headScanner, err = s.scannerAt(name, head.Seq)
		if err != nil {
			tmp.Close()
			return 0, err
		}
		defer headScanner.Close()
	}
	var line bytes.Buffer
	encoder := json.NewEncoder(&line)
	encoder.SetEscapeHTML(false)
	for _, entry := range manifest.Files {
		line.Reset()
		if err := encoder.Encode(entry); err != nil {
			tmp.Close()
			return 0, err
		}
		if _, err := tmp.Write(line.Bytes()); err != nil {
			tmp.Close()
			return 0, err
		}
		if identical {
			// 逐行比对，避免把 head 整份读进内存
			if !headScanner.Scan() || !bytes.Equal(headScanner.Bytes(), bytes.TrimSuffix(line.Bytes(), []byte("\n"))) {
				identical = false
			}
		}
	}
	if identical && headScanner.Scan() {
		identical = false
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return 0, err
	}
	if err := tmp.Close(); err != nil {
		return 0, err
	}
	if identical {
		return head.Seq, nil
	}

	if err := os.Rename(tmp.Name(), s.file(name, seq)); err != nil {
		return 0, err
	}
	// rename 之后同步目录，否则崩溃时文件名可能先于内容落盘
	if err := syncDir(s.dir(name)); err != nil {
		return 0, err
	}
	s.prune(name, seq-KeepVersions)
	return seq, nil
}

// scannerAt 打开 head 并跳过首行，返回定位到第一条目的扫描器。
func (s *SnapshotStore) scannerAt(name string, seq int64) (*lineScanner, error) {
	file, err := os.Open(s.file(name, seq))
	if err != nil {
		return nil, err
	}
	scanner := newScanner(file)
	if !scanner.Scan() {
		file.Close()
		if err := scanner.Err(); err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("%s/%d: empty snapshot", name, seq)
	}
	return &lineScanner{Scanner: scanner, file: file}, nil
}

// prune 删除 seq <= maxSeq 的旧快照。删除失败不影响本次提交，下次提交会重试。
func (s *SnapshotStore) prune(name string, maxSeq int64) {
	if maxSeq < 1 {
		return
	}
	entries, err := os.ReadDir(s.dir(name))
	if err != nil {
		return
	}
	for _, entry := range entries {
		seq, ok := parseSeq(entry.Name())
		if ok && seq <= maxSeq {
			_ = os.Remove(filepath.Join(s.dir(name), entry.Name()))
		}
	}
}

// EachManifest 依次把每个存活快照的 manifest 交给回调，内存中同时只有一个。
func (s *SnapshotStore) EachManifest(fn func(Manifest) error) error {
	entries, err := os.ReadDir(s.root)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		metas, damaged, err := s.List(entry.Name(), 0)
		if err != nil {
			return err
		}
		if len(damaged) > 0 {
			// 回收会删 blob，读不出引用的快照不能当作没人引用
			return fmt.Errorf("namespace %q has damaged snapshot %s", entry.Name(), damaged[0])
		}
		for _, meta := range metas {
			manifest, err := s.readManifest(entry.Name(), meta.Seq)
			if err != nil {
				return err
			}
			if err := fn(manifest); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *SnapshotStore) readManifest(name string, seq int64) (Manifest, error) {
	snapshot, err := s.Open(name, seq)
	if err != nil {
		return Manifest{}, err
	}
	if snapshot == nil {
		return Manifest{}, fmt.Errorf("%s/%d: missing snapshot", name, seq)
	}
	defer snapshot.Close()
	var manifest Manifest
	for snapshot.scanner.Scan() {
		var entry Entry
		if err := json.Unmarshal(snapshot.scanner.Bytes(), &entry); err != nil {
			return Manifest{}, fmt.Errorf("%s/%d: %w", name, seq, err)
		}
		manifest.Files = append(manifest.Files, entry)
	}
	return manifest, snapshot.scanner.Err()
}

// Probe 在 namespaces 目录下创建并删除临时文件，供 /readyz 使用。
func (s *SnapshotStore) Probe() error {
	file, err := os.CreateTemp(s.root, ".probe-*")
	if err != nil {
		return err
	}
	name := file.Name()
	if _, err := file.WriteString("probe"); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Remove(name)
}

func readMeta(path string) (SnapshotMeta, error) {
	file, err := os.Open(path)
	if err != nil {
		return SnapshotMeta{}, err
	}
	defer file.Close()
	scanner := newScanner(file)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return SnapshotMeta{}, fmt.Errorf("%s: %w", filepath.Base(path), err)
		}
		return SnapshotMeta{}, fmt.Errorf("%s: empty snapshot", filepath.Base(path))
	}
	var meta SnapshotMeta
	if err := json.Unmarshal(scanner.Bytes(), &meta); err != nil {
		return SnapshotMeta{}, fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	if meta.Seq < 1 {
		return SnapshotMeta{}, fmt.Errorf("%s: invalid seq", filepath.Base(path))
	}
	return meta, nil
}

func newScanner(reader io.Reader) *bufio.Scanner {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 8<<10), maxLine)
	return scanner
}

func parseSeq(name string) (int64, bool) {
	base, ok := strings.CutSuffix(name, ".jsonl")
	if !ok {
		return 0, false
	}
	seq, err := strconv.ParseInt(base, 10, 64)
	if err != nil || seq < 1 {
		return 0, false
	}
	return seq, true
}

// lineScanner 是带文件句柄的扫描器，用于逐行比对 head。
type lineScanner struct {
	*bufio.Scanner
	file *os.File
}

func (l *lineScanner) Close() error { return l.file.Close() }
