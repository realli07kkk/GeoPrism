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

func (s nsInfoStub) ResolvedZoneText() string {
	return ""
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

type nsInfoZoneStub struct{}

func (s nsInfoZoneStub) ServerCount() int {
	return 0
}

func (s nsInfoZoneStub) ServerAt(i int) any {
	return nil
}

func (s nsInfoZoneStub) QueryTimeMs() int64 {
	return 12
}

func (s nsInfoZoneStub) ResolvedZoneText() string {
	return "example.com"
}

func (s nsInfoZoneStub) IsAvailable() bool {
	return true
}

func (s nsInfoZoneStub) ErrorText() string {
	return ""
}

func TestWriteNSInfoPlainIncludesResolvedZone(t *testing.T) {
	var buffer bytes.Buffer

	writeNSInfoPlain(&buffer, nsInfoZoneStub{})

	if got := buffer.String(); !bytes.Contains([]byte(got), []byte("实际命中 Zone: example.com")) {
		t.Fatalf("output should contain resolved zone, got: %s", got)
	}
}
