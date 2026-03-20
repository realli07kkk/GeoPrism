package render

import (
	"bytes"
	"testing"
)

type nsInfoStub struct{}

func (s nsInfoStub) ServerCount() int {
	return 0
}

func (s nsInfoStub) ServerAt(i int) any {
	return nil
}

func (s nsInfoStub) QueryTimeMs() int64 {
	return 0
}

func (s nsInfoStub) IsAvailable() bool {
	return false
}

func (s nsInfoStub) ErrorText() string {
	return ""
}

func TestWriteNSInfoTypedNil(t *testing.T) {
	var buffer bytes.Buffer
	var data *nsInfoStub

	WriteNSInfo(&buffer, data)

	if buffer.Len() != 0 {
		t.Fatalf("buffer len = %d, want 0", buffer.Len())
	}
}

func TestWriteNSInfoNil(t *testing.T) {
	var buffer bytes.Buffer

	WriteNSInfo(&buffer, nil)

	if buffer.Len() != 0 {
		t.Fatalf("buffer len = %d, want 0", buffer.Len())
	}
}
