//go:build unix

package core

import (
	"os"
	"syscall"
)

// FileLock 是跨进程的劝告锁：提交持共享锁，垃圾回收持独占锁。
// 同一进程重复取锁会与自己的独占锁互等，所以两者不能在同一进程内并发。
type FileLock struct {
	file *os.File
}

func LockFile(path string, exclusive bool) (*FileLock, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	how := syscall.LOCK_SH
	if exclusive {
		how = syscall.LOCK_EX
	}
	for {
		err = syscall.Flock(int(file.Fd()), how)
		if err != syscall.EINTR {
			break
		}
	}
	if err != nil {
		file.Close()
		return nil, err
	}
	return &FileLock{file: file}, nil
}

func (l *FileLock) Close() error {
	_ = syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
	return l.file.Close()
}
