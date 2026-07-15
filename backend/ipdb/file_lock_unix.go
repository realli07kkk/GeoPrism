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
	lock, busy, err := acquireFileLockMode(path, exclusive, false)
	if busy {
		return nil, fmt.Errorf("获取文件锁失败: 锁已被占用")
	}
	return lock, err
}

// tryAcquireFileLock 非阻塞获取文件锁。busy=true 只表示锁已被其它 owner 占用；
// 打开锁文件、权限或其它 flock 错误仍通过 err 返回。
func tryAcquireFileLock(path string, exclusive bool) (*fileLock, bool, error) {
	return acquireFileLockMode(path, exclusive, true)
}

func acquireFileLockMode(path string, exclusive, nonBlocking bool) (*fileLock, bool, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, false, fmt.Errorf("打开锁文件失败: %w", err)
	}

	how := unix.LOCK_SH
	if exclusive {
		how = unix.LOCK_EX
	}
	if nonBlocking {
		how |= unix.LOCK_NB
	}
	if err := flockRetry(int(file.Fd()), how); err != nil {
		_ = file.Close()
		if nonBlocking && (errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN)) {
			return nil, true, nil
		}
		return nil, false, fmt.Errorf("获取文件锁失败: %w", err)
	}
	return &fileLock{file: file}, false, nil
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
