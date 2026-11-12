package core

import "os"

// syncDir 让目录项（新建或改名）落盘；rename 成功后必须调用。
func syncDir(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
