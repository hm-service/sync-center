//go:build !unix

package core

import "os"

// FileLock 在不支持 flock 的平台上退化为空锁；服务端只按类 Unix 系统部署。
type FileLock struct {
	file *os.File
}

func LockFile(path string, exclusive bool) (*FileLock, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	return &FileLock{file: file}, nil
}

func (l *FileLock) Close() error { return l.file.Close() }
