package ipdb

import "fmt"

// silentLogger 屏蔽 Pebble 默认的普通日志输出，避免污染 CLI 结果。
type silentLogger struct{}

func (silentLogger) Infof(string, ...interface{}) {}

func (silentLogger) Errorf(string, ...interface{}) {}

func (silentLogger) Fatalf(format string, args ...interface{}) {
	panic(fmt.Sprintf(format, args...))
}
