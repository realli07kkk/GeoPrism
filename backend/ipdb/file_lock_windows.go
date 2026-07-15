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
	lock, busy, err := acquireFileLockMode(path, exclusive, false)
	if busy {
		return nil, fmt.Errorf("获取文件锁失败: 锁已被占用")
	}
	return lock, err
}

// tryAcquireFileLock 非阻塞获取文件锁。busy=true 只表示锁已被其它 owner 占用；
// 打开锁文件、权限或其它 LockFileEx 错误仍通过 err 返回。
func tryAcquireFileLock(path string, exclusive bool) (*fileLock, bool, error) {
	return acquireFileLockMode(path, exclusive, true)
}

func acquireFileLockMode(path string, exclusive, nonBlocking bool) (*fileLock, bool, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, false, fmt.Errorf("打开锁文件失败: %w", err)
	}

	lock := &fileLock{file: file}
	var flags uint32
	if exclusive {
		flags = windows.LOCKFILE_EXCLUSIVE_LOCK
	}
	if nonBlocking {
		flags |= windows.LOCKFILE_FAIL_IMMEDIATELY
	}
	if err := windows.LockFileEx(windows.Handle(file.Fd()), flags, 0, 1, 0, &lock.overlapped); err != nil {
		_ = file.Close()
		if nonBlocking && errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			return nil, true, nil
		}
		return nil, false, fmt.Errorf("获取文件锁失败: %w", err)
	}
	return lock, false, nil
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
