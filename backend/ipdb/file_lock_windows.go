//go:build windows

package ipdb

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

// fileLock 是跨进程文件锁。共享锁供当前版本 reader 持有，独占锁供 builder 发布与回收。
type fileLock struct {
	file       *os.File
	overlapped windows.Overlapped
}

func acquireFileLock(path string, exclusive bool) (*fileLock, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("打开锁文件失败: %w", err)
	}

	lock := &fileLock{file: file}
	var flags uint32
	if exclusive {
		flags = windows.LOCKFILE_EXCLUSIVE_LOCK
	}
	if err := windows.LockFileEx(windows.Handle(file.Fd()), flags, 0, 1, 0, &lock.overlapped); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("获取文件锁失败: %w", err)
	}
	return lock, nil
}

func (l *fileLock) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	file := l.file
	l.file = nil
	unlockErr := windows.UnlockFileEx(windows.Handle(file.Fd()), 0, 1, 0, &l.overlapped)
	closeErr := file.Close()
	return errors.Join(unlockErr, closeErr)
}
