//go:build !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris && !windows

package ipdb

import "fmt"

type fileLock struct{}

func acquireFileLock(path string, exclusive bool) (*fileLock, error) {
	return nil, fmt.Errorf("当前操作系统不支持 IPDB 生命周期文件锁")
}

func (l *fileLock) Close() error {
	return nil
}
