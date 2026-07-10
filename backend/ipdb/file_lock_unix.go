//go:build darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package ipdb

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// fileLock 是跨进程文件锁。共享锁供当前版本 reader 持有，独占锁供 builder 发布与回收。
type fileLock struct {
	file *os.File
}

func acquireFileLock(path string, exclusive bool) (*fileLock, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("打开锁文件失败: %w", err)
	}

	how := unix.LOCK_SH
	if exclusive {
		how = unix.LOCK_EX
	}
	if err := flockRetry(int(file.Fd()), how); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("获取文件锁失败: %w", err)
	}
	return &fileLock{file: file}, nil
}

func (l *fileLock) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	file := l.file
	l.file = nil
	unlockErr := flockRetry(int(file.Fd()), unix.LOCK_UN)
	closeErr := file.Close()
	return errors.Join(unlockErr, closeErr)
}

func flockRetry(fd, how int) error {
	for {
		err := unix.Flock(fd, how)
		if err != unix.EINTR {
			return err
		}
	}
}
