package ipdb

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"
)

func TestOpenOverlayCreatesMetadataAndReopens(t *testing.T) {
	rootDir := t.TempDir()

	store, err := OpenOverlay(rootDir)
	if err != nil {
		t.Fatalf("OpenOverlay() error = %v", err)
	}
	if store.metadata.FormatVersion != overlayFormatVersion {
		t.Fatalf("FormatVersion = %d, want %d", store.metadata.FormatVersion, overlayFormatVersion)
	}
	if store.metadata.CreatedAt.IsZero() {
		t.Fatal("CreatedAt 不应为零值")
	}
	createdAt := store.metadata.CreatedAt

	for _, path := range []string{
		filepath.Join(rootDir, overlayDirName, overlayLockFileName),
		filepath.Join(rootDir, overlayDirName, dbDirName),
	} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("os.Stat(%q) error = %v", path, err)
		}
	}

	if err := store.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}

	reopened, err := OpenOverlay(rootDir)
	if err != nil {
		t.Fatalf("reopen OpenOverlay() error = %v", err)
	}
	defer reopened.Close()
	if !reopened.metadata.CreatedAt.Equal(createdAt) {
		t.Fatalf("reopened CreatedAt = %s, want %s", reopened.metadata.CreatedAt, createdAt)
	}
}

func TestOpenOverlayMetadataWriteFailureClosesDBBeforeLock(t *testing.T) {
	wantErr := errors.New("metadata set failed")
	var events []string
	deps := defaultOverlayDependencies()
	deps.acquireLock = func(string) (io.Closer, bool, error) {
		return funcCloser(func() error {
			events = append(events, "lock")
			return nil
		}), false, nil
	}
	deps.openDB = func(string, *pebble.Options) (overlayDB, error) {
		return &testOverlayDB{
			set: func(key, value []byte, opts *pebble.WriteOptions) error {
				if !bytes.Equal(key, metadataKey) || !opts.GetSync() {
					t.Fatalf("metadata Set 参数不符: key=%q sync=%v", key, opts.GetSync())
				}
				return wantErr
			},
			close: func() error {
				events = append(events, "db")
				return nil
			},
		}, nil
	}

	store, err := openOverlay(t.TempDir(), deps)
	if store != nil {
		_ = store.Close()
		t.Fatal("store != nil")
	}
	if !errors.Is(err, wantErr) {
		t.Fatalf("openOverlay() error = %v, want %v", err, wantErr)
	}
	if got := strings.Join(events, ","); got != "db,lock" {
		t.Fatalf("失败资源关闭顺序 = %q, want db,lock", got)
	}
}

func TestOpenOverlayReturnsLockedWithoutWaiting(t *testing.T) {
	rootDir := t.TempDir()
	first, err := OpenOverlay(rootDir)
	if err != nil {
		t.Fatalf("first OpenOverlay() error = %v", err)
	}
	defer first.Close()

	second, err := OpenOverlay(rootDir)
	if second != nil {
		_ = second.Close()
		t.Fatal("second OpenOverlay() store != nil")
	}
	if !errors.Is(err, ErrOverlayLocked) {
		t.Fatalf("second OpenOverlay() error = %v, want ErrOverlayLocked", err)
	}
}

func TestOpenOverlayDoesNotQuarantineLockedCorruptMetadata(t *testing.T) {
	rootDir := t.TempDir()
	corruptMetadata := []byte("{")
	first, err := OpenOverlay(rootDir)
	if err != nil {
		t.Fatalf("first OpenOverlay() error = %v", err)
	}
	if err := first.db.Set(metadataKey, corruptMetadata, pebble.Sync); err != nil {
		_ = first.Close()
		t.Fatalf("corrupt metadata Set() error = %v", err)
	}

	second, err := OpenOverlay(rootDir)
	if second != nil {
		_ = second.Close()
		_ = first.Close()
		t.Fatal("second OpenOverlay() store != nil")
	}
	if !errors.Is(err, ErrOverlayLocked) {
		_ = first.Close()
		t.Fatalf("second OpenOverlay() error = %v, want ErrOverlayLocked", err)
	}
	assertOverlayNotQuarantined(t, rootDir)
	metadataValue, err := readOverlayDBValue(first.db, metadataKey)
	if err != nil {
		_ = first.Close()
		t.Fatalf("读取锁住库的 metadata 失败: %v", err)
	}
	if !bytes.Equal(metadataValue, corruptMetadata) {
		_ = first.Close()
		t.Fatalf("锁冲突后 metadata = %q, want %q", metadataValue, corruptMetadata)
	}

	if err := first.Close(); err != nil {
		t.Fatalf("first Close() error = %v", err)
	}
	recovered, err := OpenOverlay(rootDir)
	if err != nil {
		t.Fatalf("recovery OpenOverlay() error = %v", err)
	}
	defer recovered.Close()
	if got := len(overlayQuarantineDirs(t, rootDir)); got != 1 {
		t.Fatalf("quarantine count after unlock = %d, want 1", got)
	}
}

func TestOpenOverlayLockAcrossProcessesAndProcessExit(t *testing.T) {
	const helperEnv = "GEOPRISM_OVERLAY_LOCK_HOLDER"
	if os.Getenv(helperEnv) == "1" {
		rootDir := os.Getenv("GEOPRISM_IPDB_ROOT")
		readyFile := os.Getenv("GEOPRISM_IPDB_READY_FILE")
		store, err := OpenOverlay(rootDir)
		if err != nil {
			t.Fatalf("helper OpenOverlay() error = %v", err)
		}
		defer store.Close()
		if err := os.WriteFile(readyFile, []byte("ready"), 0644); err != nil {
			t.Fatalf("helper 写 ready 标记失败: %v", err)
		}
		// 读取到 EOF 即退出；父测试异常终止时管道会自动关闭，不遗留长寿命子进程。
		_, _ = io.Copy(io.Discard, os.Stdin)
		return
	}

	rootDir := t.TempDir()
	readyFile := filepath.Join(rootDir, "overlay-lock-ready")
	cmd := exec.Command(os.Args[0], "-test.run=^TestOpenOverlayLockAcrossProcessesAndProcessExit$", "-test.count=1")
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	cmd.Env = append(os.Environ(),
		helperEnv+"=1",
		"GEOPRISM_IPDB_ROOT="+rootDir,
		"GEOPRISM_IPDB_READY_FILE="+readyFile,
	)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("创建 overlay lock holder stdin 失败: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("启动 overlay lock holder 失败: %v", err)
	}
	t.Cleanup(func() {
		_ = stdin.Close()
		if cmd.Process != nil && cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	})

	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(readyFile); err == nil {
			break
		} else if !os.IsNotExist(err) {
			t.Fatalf("检查 ready 标记失败: %v", err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("等待 overlay lock holder 超时\n%s", output.String())
		}
		time.Sleep(5 * time.Millisecond)
	}

	startedAt := time.Now()
	store, err := OpenOverlay(rootDir)
	if store != nil {
		_ = store.Close()
		t.Fatal("locked OpenOverlay() store != nil")
	}
	if !errors.Is(err, ErrOverlayLocked) {
		t.Fatalf("locked OpenOverlay() error = %v, want ErrOverlayLocked", err)
	}
	if elapsed := time.Since(startedAt); elapsed > time.Second {
		t.Fatalf("lock 冲突未 fail-fast: elapsed=%s", elapsed)
	}

	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("终止 overlay lock holder 失败: %v", err)
	}
	_ = cmd.Wait()
	reopened, err := OpenOverlay(rootDir)
	if err != nil {
		t.Fatalf("holder 退出后 OpenOverlay() error = %v\n%s", err, output.String())
	}
	defer reopened.Close()
}

func TestOpenOverlayLockFileErrorIsNotLocked(t *testing.T) {
	rootDir := t.TempDir()
	lockPath := filepath.Join(rootDir, overlayDirName, overlayLockFileName)
	if err := os.MkdirAll(lockPath, 0755); err != nil {
		t.Fatalf("os.MkdirAll(lockPath) error = %v", err)
	}

	store, err := OpenOverlay(rootDir)
	if store != nil {
		_ = store.Close()
		t.Fatal("OpenOverlay() store != nil")
	}
	if err == nil {
		t.Fatal("OpenOverlay() error = nil")
	}
	if errors.Is(err, ErrOverlayLocked) {
		t.Fatalf("OpenOverlay() error = %v, ordinary lock-file error 不应映射 ErrOverlayLocked", err)
	}
	if got := len(overlayQuarantineDirs(t, rootDir)); got != 0 {
		t.Fatalf("quarantine count = %d, want 0", got)
	}
}

func TestOpenOverlayQuarantinesInvalidMetadata(t *testing.T) {
	validCreatedAt := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	evidenceAddr := netip.MustParseAddr("192.0.2.200")
	evidenceRecord := Record{
		Country:       "Evidence Country",
		CountryCode:   "EC",
		Continent:     "Evidence Continent",
		ContinentCode: "EV",
		ASN:           "AS64500",
		ASName:        "Evidence ASN",
		ASDomain:      "evidence.example",
	}
	evidenceMeta := OverlayMeta{Source: "quarantine-evidence"}
	wrongVersion, err := json.Marshal(OverlayMetadata{
		FormatVersion: overlayFormatVersion + 1,
		CreatedAt:     validCreatedAt,
	})
	if err != nil {
		t.Fatalf("json.Marshal(wrongVersion) error = %v", err)
	}
	zeroCreatedAt, err := json.Marshal(OverlayMetadata{FormatVersion: overlayFormatVersion})
	if err != nil {
		t.Fatalf("json.Marshal(zeroCreatedAt) error = %v", err)
	}

	tests := []struct {
		name          string
		metadataValue []byte
	}{
		{name: "missing"},
		{name: "bad-json", metadataValue: []byte("{")},
		{name: "wrong-version", metadataValue: wrongVersion},
		{name: "zero-created-at", metadataValue: zeroCreatedAt},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rootDir := t.TempDir()
			writeRawOverlayMetadata(t, rootDir, tt.metadataValue)
			writeRawOverlayRecord(t,
				filepath.Join(rootDir, overlayDirName, dbDirName),
				evidenceAddr, evidenceRecord, evidenceMeta,
			)

			store, err := OpenOverlay(rootDir)
			if err != nil {
				t.Fatalf("OpenOverlay() error = %v", err)
			}
			defer store.Close()

			if store.metadata.FormatVersion != overlayFormatVersion || store.metadata.CreatedAt.IsZero() {
				t.Fatalf("fresh metadata = %+v", store.metadata)
			}
			quarantines := overlayQuarantineDirs(t, rootDir)
			if len(quarantines) != 1 {
				t.Fatalf("quarantine count = %d, want 1: %v", len(quarantines), quarantines)
			}
			if strings.ContainsAny(filepath.Base(quarantines[0]), `<>:"/\|?*`) {
				t.Fatalf("quarantine 名称不兼容 Windows: %q", filepath.Base(quarantines[0]))
			}
			assertRawOverlayEvidence(t, quarantines[0], tt.metadataValue,
				evidenceAddr, evidenceRecord, evidenceMeta)

			if record, metadata, matched, err := store.Get(evidenceAddr, time.Time{}); err != nil || matched || record != (Record{}) || metadata != (OverlayMeta{}) {
				t.Fatalf("fresh DB 不应混入 quarantine 记录: (%+v, %+v, %v, %v)",
					record, metadata, matched, err)
			}
			freshRecord := Record{CountryCode: "FRESH", ASN: "AS64505"}
			freshMeta := OverlayMeta{Source: "fresh"}
			if err := store.Put(evidenceAddr, freshRecord, freshMeta); err != nil {
				t.Fatalf("fresh DB Put() error = %v", err)
			}
			record, metadata, matched, err := store.Get(evidenceAddr, time.Time{})
			freshRecord.Network = "192.0.2.200/32"
			if err != nil || !matched || record != freshRecord || metadata != freshMeta {
				t.Fatalf("fresh DB Get() = (%+v, %+v, %v, %v)",
					record, metadata, matched, err)
			}
		})
	}
}

func TestOpenOverlayQuarantineNamesAreUnique(t *testing.T) {
	rootDir := t.TempDir()
	writeRawOverlayMetadata(t, rootDir, []byte("{"))

	fixedNow := time.Date(2026, 7, 15, 12, 34, 56, 123456789, time.UTC)
	deps := defaultOverlayDependencies()
	deps.now = func() time.Time { return fixedNow }

	first, err := openOverlay(rootDir, deps)
	if err != nil {
		t.Fatalf("first openOverlay() error = %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("first Close() error = %v", err)
	}

	writeActiveOverlayMetadata(t, rootDir, []byte("{"))
	second, err := openOverlay(rootDir, deps)
	if err != nil {
		t.Fatalf("second openOverlay() error = %v", err)
	}
	defer second.Close()

	quarantines := overlayQuarantineDirs(t, rootDir)
	if len(quarantines) != 2 {
		t.Fatalf("quarantine count = %d, want 2: %v", len(quarantines), quarantines)
	}
	if filepath.Base(quarantines[0]) == filepath.Base(quarantines[1]) {
		t.Fatalf("quarantine 名称重复: %v", quarantines)
	}
}

func TestOpenOverlayQuarantineSkipsDanglingSymlink(t *testing.T) {
	rootDir := t.TempDir()
	writeRawOverlayMetadata(t, rootDir, []byte("{"))

	fixedNow := time.Date(2026, 7, 15, 12, 34, 56, 123456789, time.UTC)
	overlayDir := filepath.Join(rootDir, overlayDirName)
	occupied, err := nextOverlayQuarantineDir(overlayDir, fixedNow)
	if err != nil {
		t.Fatalf("nextOverlayQuarantineDir() error = %v", err)
	}
	if err := os.Symlink("missing-target", occupied); err != nil {
		t.Skipf("当前环境无法创建 dangling symlink: %v", err)
	}

	deps := defaultOverlayDependencies()
	deps.now = func() time.Time { return fixedNow }
	store, err := openOverlay(rootDir, deps)
	if err != nil {
		t.Fatalf("openOverlay() error = %v", err)
	}
	defer store.Close()

	quarantineDir := occupied + "-1"
	info, err := os.Stat(quarantineDir)
	if err != nil {
		t.Fatalf("os.Stat(%q) error = %v", quarantineDir, err)
	}
	if !info.IsDir() {
		t.Fatalf("quarantine path %q 不是目录", quarantineDir)
	}
	linkInfo, err := os.Lstat(occupied)
	if err != nil || linkInfo.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("原 dangling symlink 未保留: info=%v err=%v", linkInfo, err)
	}
}

func TestOpenOverlayQuarantinesPebbleCorruptionDuringOpenOrMetadataRead(t *testing.T) {
	t.Run("open", func(t *testing.T) {
		rootDir := t.TempDir()
		writeRawOverlayMetadata(t, rootDir, validOverlayMetadataJSON(t))

		deps := defaultOverlayDependencies()
		realOpen := deps.openDB
		openCalls := 0
		deps.openDB = func(path string, opts *pebble.Options) (overlayDB, error) {
			openCalls++
			if openCalls == 1 {
				return nil, pebble.ErrCorruption
			}
			return realOpen(path, opts)
		}

		store, err := openOverlay(rootDir, deps)
		if err != nil {
			t.Fatalf("openOverlay() error = %v", err)
		}
		defer store.Close()
		if got := len(overlayQuarantineDirs(t, rootDir)); got != 1 {
			t.Fatalf("quarantine count = %d, want 1", got)
		}
	})

	t.Run("metadata-get", func(t *testing.T) {
		rootDir := t.TempDir()
		writeRawOverlayMetadata(t, rootDir, validOverlayMetadataJSON(t))

		deps := defaultOverlayDependencies()
		realOpen := deps.openDB
		openCalls := 0
		deps.openDB = func(path string, opts *pebble.Options) (overlayDB, error) {
			db, err := realOpen(path, opts)
			if err != nil {
				return nil, err
			}
			openCalls++
			if openCalls == 1 {
				return &testOverlayDB{
					overlayDB: db,
					get: func([]byte) ([]byte, io.Closer, error) {
						return nil, nil, pebble.ErrCorruption
					},
				}, nil
			}
			return db, nil
		}

		store, err := openOverlay(rootDir, deps)
		if err != nil {
			t.Fatalf("openOverlay() error = %v", err)
		}
		defer store.Close()
		if got := len(overlayQuarantineDirs(t, rootDir)); got != 1 {
			t.Fatalf("quarantine count = %d, want 1", got)
		}
	})
}

func TestOpenOverlayDoesNotQuarantineOrdinaryOrCloseErrors(t *testing.T) {
	evidenceAddr := netip.MustParseAddr("192.0.2.201")
	evidenceRecord := Record{CountryCode: "EC", ASN: "AS64501", ASName: "preserved"}
	evidenceMeta := OverlayMeta{Source: "preserved"}
	seedEvidence := func(t *testing.T, rootDir string, metadataValue []byte) {
		t.Helper()
		writeRawOverlayMetadata(t, rootDir, metadataValue)
		writeRawOverlayRecord(t,
			filepath.Join(rootDir, overlayDirName, dbDirName),
			evidenceAddr, evidenceRecord, evidenceMeta,
		)
	}
	assertEvidence := func(t *testing.T, rootDir string, metadataValue []byte) {
		t.Helper()
		assertOverlayNotQuarantined(t, rootDir)
		assertRawOverlayEvidence(t,
			filepath.Join(rootDir, overlayDirName, dbDirName),
			metadataValue, evidenceAddr, evidenceRecord, evidenceMeta,
		)
	}

	t.Run("ordinary-open-error", func(t *testing.T) {
		rootDir := t.TempDir()
		metadataValue := validOverlayMetadataJSON(t)
		seedEvidence(t, rootDir, metadataValue)
		wantErr := errors.New("open failed")

		deps := defaultOverlayDependencies()
		deps.openDB = func(string, *pebble.Options) (overlayDB, error) {
			return nil, wantErr
		}

		store, err := openOverlay(rootDir, deps)
		if store != nil {
			_ = store.Close()
			t.Fatal("store != nil")
		}
		if !errors.Is(err, wantErr) {
			t.Fatalf("error = %v, want %v", err, wantErr)
		}
		assertEvidence(t, rootDir, metadataValue)
	})

	t.Run("metadata-closer-error", func(t *testing.T) {
		rootDir := t.TempDir()
		metadataValue := validOverlayMetadataJSON(t)
		seedEvidence(t, rootDir, metadataValue)
		wantErr := errors.New("closer failed")

		deps := defaultOverlayDependencies()
		realOpen := deps.openDB
		deps.openDB = func(path string, opts *pebble.Options) (overlayDB, error) {
			db, err := realOpen(path, opts)
			if err != nil {
				return nil, err
			}
			return &testOverlayDB{
				overlayDB: db,
				get: func(key []byte) ([]byte, io.Closer, error) {
					value, closer, err := db.Get(key)
					if err != nil {
						return nil, nil, err
					}
					return value, &closeErrorCloser{Closer: closer, err: wantErr}, nil
				},
			}, nil
		}

		store, err := openOverlay(rootDir, deps)
		if store != nil {
			_ = store.Close()
			t.Fatal("store != nil")
		}
		if !errors.Is(err, wantErr) {
			t.Fatalf("error = %v, want %v", err, wantErr)
		}
		assertEvidence(t, rootDir, metadataValue)
	})

	t.Run("database-close-error", func(t *testing.T) {
		rootDir := t.TempDir()
		metadataValue := []byte("{")
		seedEvidence(t, rootDir, metadataValue)
		wantErr := errors.New("database close failed")

		deps := defaultOverlayDependencies()
		realOpen := deps.openDB
		deps.openDB = func(path string, opts *pebble.Options) (overlayDB, error) {
			db, err := realOpen(path, opts)
			if err != nil {
				return nil, err
			}
			return &testOverlayDB{
				overlayDB: db,
				close: func() error {
					return errors.Join(db.Close(), wantErr)
				},
			}, nil
		}

		store, err := openOverlay(rootDir, deps)
		if store != nil {
			_ = store.Close()
			t.Fatal("store != nil")
		}
		if !errors.Is(err, wantErr) {
			t.Fatalf("error = %v, want %v", err, wantErr)
		}
		assertEvidence(t, rootDir, metadataValue)
	})

	t.Run("rename-error", func(t *testing.T) {
		rootDir := t.TempDir()
		metadataValue := []byte("{")
		seedEvidence(t, rootDir, metadataValue)
		wantErr := errors.New("rename failed")

		deps := defaultOverlayDependencies()
		deps.rename = func(string, string) error { return wantErr }

		store, err := openOverlay(rootDir, deps)
		if store != nil {
			_ = store.Close()
			t.Fatal("store != nil")
		}
		if !errors.Is(err, wantErr) {
			t.Fatalf("error = %v, want %v", err, wantErr)
		}
		assertEvidence(t, rootDir, metadataValue)
	})
}

func TestOpenOverlayKeepsQuarantineWhenFreshRebuildFails(t *testing.T) {
	rootDir := t.TempDir()
	corruptMetadata := []byte("{")
	evidenceAddr := netip.MustParseAddr("192.0.2.202")
	evidenceRecord := Record{CountryCode: "EC", ASN: "AS64502"}
	evidenceMeta := OverlayMeta{Source: "fresh-failure-evidence"}
	writeRawOverlayMetadata(t, rootDir, corruptMetadata)
	writeRawOverlayRecord(t,
		filepath.Join(rootDir, overlayDirName, dbDirName),
		evidenceAddr, evidenceRecord, evidenceMeta,
	)
	wantErr := errors.New("fresh open failed")

	deps := defaultOverlayDependencies()
	realOpen := deps.openDB
	openCalls := 0
	deps.openDB = func(path string, opts *pebble.Options) (overlayDB, error) {
		openCalls++
		if openCalls == 2 {
			return nil, wantErr
		}
		return realOpen(path, opts)
	}

	store, err := openOverlay(rootDir, deps)
	if store != nil {
		_ = store.Close()
		t.Fatal("store != nil")
	}
	if !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want %v", err, wantErr)
	}
	if !strings.Contains(err.Error(), "解析 overlay 元数据失败") ||
		!strings.Contains(err.Error(), "overlay 已隔离到") {
		t.Fatalf("error 缺少原始损坏原因或 quarantine 路径: %v", err)
	}
	quarantines := overlayQuarantineDirs(t, rootDir)
	if len(quarantines) != 1 {
		t.Fatalf("quarantine count = %d, want 1", len(quarantines))
	}
	if !strings.Contains(err.Error(), quarantines[0]) {
		t.Fatalf("error 未包含实际 quarantine 路径 %q: %v", quarantines[0], err)
	}
	assertRawOverlayEvidence(t, quarantines[0], corruptMetadata,
		evidenceAddr, evidenceRecord, evidenceMeta)
}

func TestOpenOverlayKeepsQuarantineWhenFreshMetadataWriteFails(t *testing.T) {
	rootDir := t.TempDir()
	corruptMetadata := []byte("{")
	evidenceAddr := netip.MustParseAddr("192.0.2.203")
	evidenceRecord := Record{CountryCode: "EC", ASN: "AS64503"}
	evidenceMeta := OverlayMeta{Source: "metadata-failure-evidence"}
	writeRawOverlayMetadata(t, rootDir, corruptMetadata)
	writeRawOverlayRecord(t,
		filepath.Join(rootDir, overlayDirName, dbDirName),
		evidenceAddr, evidenceRecord, evidenceMeta,
	)

	wantErr := errors.New("fresh metadata set failed")
	dbClosed := false
	deps := defaultOverlayDependencies()
	realOpen := deps.openDB
	openCalls := 0
	deps.openDB = func(path string, opts *pebble.Options) (overlayDB, error) {
		db, err := realOpen(path, opts)
		if err != nil {
			return nil, err
		}
		openCalls++
		if openCalls != 2 {
			return db, nil
		}
		return &testOverlayDB{
			overlayDB: db,
			set: func(key, value []byte, opts *pebble.WriteOptions) error {
				if bytes.Equal(key, metadataKey) {
					return wantErr
				}
				return db.Set(key, value, opts)
			},
			close: func() error {
				dbClosed = true
				return db.Close()
			},
		}, nil
	}

	store, err := openOverlay(rootDir, deps)
	if store != nil {
		_ = store.Close()
		t.Fatal("store != nil")
	}
	if !errors.Is(err, wantErr) ||
		!strings.Contains(err.Error(), "解析 overlay 元数据失败") ||
		!strings.Contains(err.Error(), "overlay 已隔离到") {
		t.Fatalf("error 缺少失败链上下文: %v", err)
	}
	if !dbClosed {
		t.Fatal("fresh metadata 写失败后 DB 未关闭")
	}
	quarantines := overlayQuarantineDirs(t, rootDir)
	if len(quarantines) != 1 {
		t.Fatalf("quarantine count = %d, want 1", len(quarantines))
	}
	if !strings.Contains(err.Error(), quarantines[0]) {
		t.Fatalf("error 未包含实际 quarantine 路径 %q: %v", quarantines[0], err)
	}
	assertRawOverlayEvidence(t, quarantines[0], corruptMetadata,
		evidenceAddr, evidenceRecord, evidenceMeta)

	lock, busy, lockErr := tryAcquireFileLock(
		filepath.Join(rootDir, overlayDirName, overlayLockFileName), true)
	if lockErr != nil || busy || lock == nil {
		t.Fatalf("metadata 写失败后生命周期锁未释放: lock=%v busy=%v err=%v",
			lock, busy, lockErr)
	}
	if err := lock.Close(); err != nil {
		t.Fatalf("lock.Close() error = %v", err)
	}
}

func TestOverlayPutGetRoundTripIPv4AndIPv6(t *testing.T) {
	rootDir := t.TempDir()
	store, err := OpenOverlay(rootDir)
	if err != nil {
		t.Fatalf("OpenOverlay() error = %v", err)
	}

	fetchedAt := time.Date(2026, 7, 15, 12, 0, 0, 987654321, time.FixedZone("UTC+8", 8*60*60))
	expiresAt := fetchedAt.Add(24 * time.Hour)
	tests := []struct {
		addr        netip.Addr
		record      Record
		wantNetwork string
	}{
		{
			addr: netip.MustParseAddr("1.1.1.1"),
			record: Record{
				Country:       "United States",
				CountryCode:   "US",
				Continent:     "North America",
				ContinentCode: "NA",
				ASN:           "AS13335",
				ASName:        "Cloudflare, Inc.",
				ASDomain:      "cloudflare.com",
			},
			wantNetwork: "1.1.1.1/32",
		},
		{
			addr: netip.MustParseAddr("2001:db8::1"),
			record: Record{
				Country:     "Example",
				CountryCode: "EX",
				ASN:         "AS64500",
			},
			wantNetwork: "2001:db8::1/128",
		},
	}
	for _, tt := range tests {
		if err := store.Put(tt.addr, tt.record, OverlayMeta{
			Source:    "ipinfo",
			FetchedAt: fetchedAt,
			ExpiresAt: expiresAt,
		}); err != nil {
			_ = store.Close()
			t.Fatalf("Put(%s) error = %v", tt.addr, err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	reopened, err := OpenOverlay(rootDir)
	if err != nil {
		t.Fatalf("reopen OpenOverlay() error = %v", err)
	}
	defer reopened.Close()

	wantFetchedAt := time.Unix(fetchedAt.Unix(), 0).UTC()
	wantExpiresAt := time.Unix(expiresAt.Unix(), 0).UTC()
	for _, tt := range tests {
		record, metadata, matched, err := reopened.Get(tt.addr, wantExpiresAt.Add(-time.Second))
		if err != nil {
			t.Fatalf("Get(%s) error = %v", tt.addr, err)
		}
		if !matched {
			t.Fatalf("Get(%s) matched = false", tt.addr)
		}
		wantRecord := tt.record
		wantRecord.Network = tt.wantNetwork
		if record != wantRecord {
			t.Fatalf("Get(%s) record = %+v, want %+v", tt.addr, record, wantRecord)
		}
		wantMetadata := (OverlayMeta{
			Source:    "ipinfo",
			FetchedAt: wantFetchedAt,
			ExpiresAt: wantExpiresAt,
		})
		if metadata != wantMetadata {
			t.Fatalf("Get(%s) metadata = %+v, want %+v", tt.addr, metadata, wantMetadata)
		}
	}
}

func TestOverlayPutLastWriteWins(t *testing.T) {
	store, err := OpenOverlay(t.TempDir())
	if err != nil {
		t.Fatalf("OpenOverlay() error = %v", err)
	}
	defer store.Close()

	addr := netip.MustParseAddr("8.8.8.8")
	if err := store.Put(addr, Record{CountryCode: "OLD", ASName: "old"}, OverlayMeta{Source: "old"}); err != nil {
		t.Fatalf("first Put() error = %v", err)
	}
	wantRecord := Record{CountryCode: "NEW", ASN: "AS15169"}
	wantMetadata := OverlayMeta{Source: "new", FetchedAt: time.Unix(123, 0).UTC()}
	if err := store.Put(addr, wantRecord, wantMetadata); err != nil {
		t.Fatalf("second Put() error = %v", err)
	}

	record, metadata, matched, err := store.Get(addr, time.Time{})
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if !matched {
		t.Fatal("Get() matched = false")
	}
	wantRecord.Network = "8.8.8.8/32"
	if record != wantRecord || metadata != wantMetadata {
		t.Fatalf("Get() = (%+v, %+v), want (%+v, %+v)", record, metadata, wantRecord, wantMetadata)
	}
}

func TestOverlayGetTTLBoundaries(t *testing.T) {
	expiresAt := time.Date(2026, 7, 16, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name      string
		now       time.Time
		wantMatch bool
	}{
		{name: "before", now: expiresAt.Add(-time.Nanosecond), wantMatch: true},
		{name: "equal", now: expiresAt},
		{name: "after", now: expiresAt.Add(time.Nanosecond)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, err := OpenOverlay(t.TempDir())
			if err != nil {
				t.Fatalf("OpenOverlay() error = %v", err)
			}
			defer store.Close()

			addr := netip.MustParseAddr("9.9.9.9")
			if err := store.Put(addr, Record{CountryCode: "US"}, OverlayMeta{ExpiresAt: expiresAt}); err != nil {
				t.Fatalf("Put() error = %v", err)
			}
			record, metadata, matched, err := store.Get(addr, tt.now)
			if err != nil {
				t.Fatalf("Get() error = %v", err)
			}
			if matched != tt.wantMatch {
				t.Fatalf("matched = %v, want %v", matched, tt.wantMatch)
			}
			if !tt.wantMatch {
				if record != (Record{}) || metadata != (OverlayMeta{}) {
					t.Fatalf("miss 返回非零值: record=%+v metadata=%+v", record, metadata)
				}
				key, _ := encodeOverlayStoreKey(addr)
				if _, closer, err := store.db.Get(key); !errors.Is(err, pebble.ErrNotFound) {
					if err == nil {
						_ = closer.Close()
					}
					t.Fatalf("expired key Get() error = %v, want ErrNotFound", err)
				}
			}
		})
	}

	t.Run("zero-never-expires", func(t *testing.T) {
		store, err := OpenOverlay(t.TempDir())
		if err != nil {
			t.Fatalf("OpenOverlay() error = %v", err)
		}
		defer store.Close()
		addr := netip.MustParseAddr("4.4.4.4")
		if err := store.Put(addr, Record{CountryCode: "US"}, OverlayMeta{}); err != nil {
			t.Fatalf("Put() error = %v", err)
		}
		_, _, matched, err := store.Get(addr, time.Date(9999, 12, 31, 0, 0, 0, 0, time.UTC))
		if err != nil || !matched {
			t.Fatalf("Get() = matched %v, error %v; want hit", matched, err)
		}
	})
}

func TestOverlayGetNotFoundReturnsZeroMiss(t *testing.T) {
	store, err := OpenOverlay(t.TempDir())
	if err != nil {
		t.Fatalf("OpenOverlay() error = %v", err)
	}
	defer store.Close()

	record, metadata, matched, err := store.Get(netip.MustParseAddr("192.0.2.1"), time.Time{})
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if matched || record != (Record{}) || metadata != (OverlayMeta{}) {
		t.Fatalf("Get() = (%+v, %+v, %v), want zero miss", record, metadata, matched)
	}
}

func TestOverlayGetDeletesCorruptValueWithoutAffectingOtherRecords(t *testing.T) {
	tests := []struct {
		name  string
		value func(*testing.T) []byte
	}{
		{
			name: "truncated",
			value: func(*testing.T) []byte {
				return []byte{0xff}
			},
		},
		{
			name: "wrong-version",
			value: func(t *testing.T) []byte {
				t.Helper()
				value, err := encodeOverlayRecordValueV1(
					Record{CountryCode: "BAD"}, OverlayMeta{Source: "bad-version"})
				if err != nil {
					t.Fatalf("encodeOverlayRecordValueV1() error = %v", err)
				}
				value[0] = overlayValueVersionV1 + 1
				return value
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, err := OpenOverlay(t.TempDir())
			if err != nil {
				t.Fatalf("OpenOverlay() error = %v", err)
			}
			defer store.Close()

			corruptAddr := netip.MustParseAddr("192.0.2.10")
			healthyAddr := netip.MustParseAddr("192.0.2.11")
			if err := store.Put(healthyAddr, Record{CountryCode: "OK"}, OverlayMeta{Source: "ipinfo"}); err != nil {
				t.Fatalf("healthy Put() error = %v", err)
			}
			corruptKey, err := encodeOverlayStoreKey(corruptAddr)
			if err != nil {
				t.Fatalf("encodeOverlayStoreKey() error = %v", err)
			}
			if err := store.db.Set(corruptKey, tt.value(t), pebble.Sync); err != nil {
				t.Fatalf("corrupt Set() error = %v", err)
			}

			record, metadata, matched, err := store.Get(corruptAddr, time.Time{})
			if err != nil {
				t.Fatalf("corrupt Get() error = %v", err)
			}
			if matched || record != (Record{}) || metadata != (OverlayMeta{}) {
				t.Fatalf("corrupt Get() = (%+v, %+v, %v), want zero miss", record, metadata, matched)
			}
			if _, closer, err := store.db.Get(corruptKey); !errors.Is(err, pebble.ErrNotFound) {
				if err == nil {
					_ = closer.Close()
				}
				t.Fatalf("corrupt key Get() error = %v, want ErrNotFound", err)
			}
			if record, _, matched, err := store.Get(healthyAddr, time.Time{}); err != nil || !matched || record.CountryCode != "OK" {
				t.Fatalf("healthy Get() = (%+v, %v, %v), want hit", record, matched, err)
			}
		})
	}
}

func TestOverlayRejectsInvalidAddressesNetworkAndReservedExpiry(t *testing.T) {
	store, err := OpenOverlay(t.TempDir())
	if err != nil {
		t.Fatalf("OpenOverlay() error = %v", err)
	}
	defer store.Close()
	realDB := store.db
	dbGetCalls := 0
	dbSetCalls := 0
	store.db = &testOverlayDB{
		overlayDB: realDB,
		get: func(key []byte) ([]byte, io.Closer, error) {
			dbGetCalls++
			return realDB.Get(key)
		},
		set: func(key, value []byte, opts *pebble.WriteOptions) error {
			dbSetCalls++
			return realDB.Set(key, value, opts)
		},
	}

	invalidAddrs := []netip.Addr{
		{},
		netip.MustParseAddr("::ffff:192.0.2.1"),
		netip.MustParseAddr("fe80::1%en0"),
	}
	for _, addr := range invalidAddrs {
		if err := store.Put(addr, Record{}, OverlayMeta{}); err == nil {
			t.Fatalf("Put(%q) error = nil", addr)
		}
		if _, _, _, err := store.Get(addr, time.Time{}); err == nil {
			t.Fatalf("Get(%q) error = nil", addr)
		}
	}
	if dbGetCalls != 0 || dbSetCalls != 0 {
		t.Fatalf("invalid addr 访问了存储: Get=%d Set=%d", dbGetCalls, dbSetCalls)
	}

	addr := netip.MustParseAddr("192.0.2.20")
	if err := store.Put(addr, Record{}, OverlayMeta{}); err != nil {
		t.Fatalf("Put(empty Network) error = %v", err)
	}
	wantRecord := Record{Network: "192.0.2.20/32", CountryCode: "KEEP", ASN: "AS64503"}
	wantMeta := OverlayMeta{Source: "sentinel"}
	if err := store.Put(addr, wantRecord, wantMeta); err != nil {
		t.Fatalf("Put(correct Network) error = %v", err)
	}
	for _, network := range []string{
		"not-a-prefix",
		"192.0.2.0/24",
		"192.0.2.21/32",
		"2001:db8::1/128",
	} {
		if err := store.Put(addr, Record{Network: network}, OverlayMeta{}); err == nil {
			t.Fatalf("Put(Network=%q) error = nil", network)
		}
	}
	if err := store.Put(addr, Record{}, OverlayMeta{ExpiresAt: time.Unix(0, 123)}); err == nil {
		t.Fatal("Put(ExpiresAt Unix 0) error = nil")
	}
	record, metadata, matched, err := store.Get(addr, time.Time{})
	if err != nil || !matched || record != wantRecord || metadata != wantMeta {
		t.Fatalf("失败 Put 改写了已有值: record=%+v metadata=%+v matched=%v err=%v",
			record, metadata, matched, err)
	}
}

func TestOverlayCleanupFailureReturnsZeroMissAndError(t *testing.T) {
	expiresAt := time.Date(2026, 7, 16, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		name  string
		setup func(*testing.T, *OverlayStore, netip.Addr)
		now   time.Time
	}{
		{
			name: "expired",
			setup: func(t *testing.T, store *OverlayStore, addr netip.Addr) {
				t.Helper()
				if err := store.Put(addr, Record{CountryCode: "US"}, OverlayMeta{ExpiresAt: expiresAt}); err != nil {
					t.Fatalf("Put() error = %v", err)
				}
			},
			now: expiresAt,
		},
		{
			name: "corrupt-value",
			setup: func(t *testing.T, store *OverlayStore, addr netip.Addr) {
				t.Helper()
				key, err := encodeOverlayStoreKey(addr)
				if err != nil {
					t.Fatalf("encodeOverlayStoreKey() error = %v", err)
				}
				if err := store.db.Set(key, []byte{0xff}, pebble.Sync); err != nil {
					t.Fatalf("Set(corrupt) error = %v", err)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store, err := OpenOverlay(t.TempDir())
			if err != nil {
				t.Fatalf("OpenOverlay() error = %v", err)
			}
			defer store.Close()

			addr := netip.MustParseAddr("192.0.2.30")
			tt.setup(t, store, addr)
			realDB := store.db
			wantErr := errors.New("delete failed")
			store.db = &testOverlayDB{
				overlayDB: realDB,
				delete: func([]byte, *pebble.WriteOptions) error {
					return wantErr
				},
			}

			record, metadata, matched, err := store.Get(addr, tt.now)
			if !errors.Is(err, wantErr) {
				t.Fatalf("Get() error = %v, want %v", err, wantErr)
			}
			if matched || record != (Record{}) || metadata != (OverlayMeta{}) {
				t.Fatalf("Get() = (%+v, %+v, %v), want zero miss", record, metadata, matched)
			}
			key, _ := encodeOverlayStoreKey(addr)
			if _, closer, err := realDB.Get(key); err != nil {
				t.Fatalf("cleanup 失败后 key 应保留: %v", err)
			} else if err := closer.Close(); err != nil {
				t.Fatalf("closer.Close() error = %v", err)
			}
		})
	}
}

func TestOverlayRuntimeCorruptionReturnsErrorWithoutQuarantine(t *testing.T) {
	t.Run("get", func(t *testing.T) {
		rootDir := t.TempDir()
		store, err := OpenOverlay(rootDir)
		if err != nil {
			t.Fatalf("OpenOverlay() error = %v", err)
		}
		defer store.Close()

		addr := netip.MustParseAddr("192.0.2.40")
		if err := store.Put(addr, Record{CountryCode: "KEEP"}, OverlayMeta{Source: "keep"}); err != nil {
			t.Fatalf("Put(sentinel) error = %v", err)
		}
		realDB := store.db
		store.db = &testOverlayDB{
			overlayDB: realDB,
			get: func([]byte) ([]byte, io.Closer, error) {
				return nil, nil, pebble.ErrCorruption
			},
		}
		_, _, matched, err := store.Get(addr, time.Time{})
		if matched || !errors.Is(err, pebble.ErrCorruption) {
			t.Fatalf("Get() = matched %v, error %v; want corruption error", matched, err)
		}
		assertOverlayNotQuarantined(t, rootDir)
		store.db = realDB
		record, metadata, matched, err := store.Get(addr, time.Time{})
		if err != nil || !matched || record.CountryCode != "KEEP" || metadata.Source != "keep" {
			t.Fatalf("corruption 后 sentinel = (%+v, %+v, %v, %v)", record, metadata, matched, err)
		}
	})

	t.Run("put", func(t *testing.T) {
		rootDir := t.TempDir()
		store, err := OpenOverlay(rootDir)
		if err != nil {
			t.Fatalf("OpenOverlay() error = %v", err)
		}
		defer store.Close()

		addr := netip.MustParseAddr("192.0.2.41")
		wantRecord := Record{CountryCode: "KEEP"}
		wantMeta := OverlayMeta{Source: "keep"}
		if err := store.Put(addr, wantRecord, wantMeta); err != nil {
			t.Fatalf("Put(sentinel) error = %v", err)
		}
		realDB := store.db
		store.db = &testOverlayDB{
			overlayDB: realDB,
			set: func([]byte, []byte, *pebble.WriteOptions) error {
				return pebble.ErrCorruption
			},
		}
		err = store.Put(addr, Record{CountryCode: "NEW"}, OverlayMeta{Source: "new"})
		if !errors.Is(err, pebble.ErrCorruption) {
			t.Fatalf("Put() error = %v, want ErrCorruption", err)
		}
		assertOverlayNotQuarantined(t, rootDir)
		store.db = realDB
		record, metadata, matched, err := store.Get(addr, time.Time{})
		wantRecord.Network = "192.0.2.41/32"
		if err != nil || !matched || record != wantRecord || metadata != wantMeta {
			t.Fatalf("corruption Put 改写了 sentinel: (%+v, %+v, %v, %v)",
				record, metadata, matched, err)
		}
	})
}

func TestOverlayGetCloserFailureReturnsZeroMissWithoutDelete(t *testing.T) {
	store, err := OpenOverlay(t.TempDir())
	if err != nil {
		t.Fatalf("OpenOverlay() error = %v", err)
	}
	defer store.Close()

	addr := netip.MustParseAddr("192.0.2.45")
	if err := store.Put(addr, Record{CountryCode: "US"}, OverlayMeta{}); err != nil {
		t.Fatalf("Put() error = %v", err)
	}
	realDB := store.db
	wantErr := errors.New("value closer failed")
	store.db = &testOverlayDB{
		overlayDB: realDB,
		get: func(key []byte) ([]byte, io.Closer, error) {
			value, closer, err := realDB.Get(key)
			if err != nil {
				return nil, nil, err
			}
			return value, &closeErrorCloser{Closer: closer, err: wantErr}, nil
		},
	}

	record, metadata, matched, err := store.Get(addr, time.Time{})
	if !errors.Is(err, wantErr) {
		t.Fatalf("Get() error = %v, want %v", err, wantErr)
	}
	if matched || record != (Record{}) || metadata != (OverlayMeta{}) {
		t.Fatalf("Get() = (%+v, %+v, %v), want zero miss", record, metadata, matched)
	}
	key, _ := encodeOverlayStoreKey(addr)
	if _, closer, err := realDB.Get(key); err != nil {
		t.Fatalf("closer 失败后 key 应保留: %v", err)
	} else if err := closer.Close(); err != nil {
		t.Fatalf("closer.Close() error = %v", err)
	}
}

func TestOverlayCopiesMetadataAndValueBeforeClosingReader(t *testing.T) {
	t.Run("metadata", func(t *testing.T) {
		rootDir := t.TempDir()
		createdAt := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
		metadataValue, err := json.Marshal(OverlayMetadata{
			FormatVersion: overlayFormatVersion,
			CreatedAt:     createdAt,
		})
		if err != nil {
			t.Fatalf("json.Marshal() error = %v", err)
		}
		writeRawOverlayMetadata(t, rootDir, metadataValue)

		deps := defaultOverlayDependencies()
		realOpen := deps.openDB
		deps.openDB = func(path string, opts *pebble.Options) (overlayDB, error) {
			db, err := realOpen(path, opts)
			if err != nil {
				return nil, err
			}
			return &testOverlayDB{
				overlayDB: db,
				get: func(key []byte) ([]byte, io.Closer, error) {
					value, closer, err := db.Get(key)
					if err != nil {
						return nil, nil, err
					}
					owned := append([]byte(nil), value...)
					if err := closer.Close(); err != nil {
						return nil, nil, err
					}
					return owned, mutatingCloser(owned), nil
				},
			}, nil
		}

		store, err := openOverlay(rootDir, deps)
		if err != nil {
			t.Fatalf("openOverlay() error = %v", err)
		}
		defer store.Close()
		if !store.metadata.CreatedAt.Equal(createdAt) {
			t.Fatalf("CreatedAt = %s, want %s", store.metadata.CreatedAt, createdAt)
		}
		if got := len(overlayQuarantineDirs(t, rootDir)); got != 0 {
			t.Fatalf("metadata reader 关闭后误隔离: quarantine count=%d", got)
		}
	})

	t.Run("value", func(t *testing.T) {
		store, err := OpenOverlay(t.TempDir())
		if err != nil {
			t.Fatalf("OpenOverlay() error = %v", err)
		}
		defer store.Close()

		addr := netip.MustParseAddr("192.0.2.46")
		wantRecord := Record{CountryCode: "COPY", ASN: "AS64504"}
		wantMeta := OverlayMeta{Source: "copy-test"}
		if err := store.Put(addr, wantRecord, wantMeta); err != nil {
			t.Fatalf("Put() error = %v", err)
		}
		realDB := store.db
		store.db = &testOverlayDB{
			overlayDB: realDB,
			get: func(key []byte) ([]byte, io.Closer, error) {
				value, closer, err := realDB.Get(key)
				if err != nil {
					return nil, nil, err
				}
				owned := append([]byte(nil), value...)
				if err := closer.Close(); err != nil {
					return nil, nil, err
				}
				return owned, mutatingCloser(owned), nil
			},
		}

		record, metadata, matched, err := store.Get(addr, time.Time{})
		if err != nil || !matched {
			t.Fatalf("Get() = matched %v, error %v", matched, err)
		}
		wantRecord.Network = "192.0.2.46/32"
		if record != wantRecord || metadata != wantMeta {
			t.Fatalf("Get() = (%+v, %+v), want (%+v, %+v)",
				record, metadata, wantRecord, wantMeta)
		}
	})
}

func TestOverlayMetadataAndPutUseSynchronousWrites(t *testing.T) {
	t.Run("metadata", func(t *testing.T) {
		deps := defaultOverlayDependencies()
		realOpen := deps.openDB
		syncSeen := false
		deps.openDB = func(path string, opts *pebble.Options) (overlayDB, error) {
			db, err := realOpen(path, opts)
			if err != nil {
				return nil, err
			}
			return &testOverlayDB{
				overlayDB: db,
				set: func(key, value []byte, opts *pebble.WriteOptions) error {
					if string(key) == string(metadataKey) {
						syncSeen = opts.GetSync()
					}
					return db.Set(key, value, opts)
				},
			}, nil
		}

		store, err := openOverlay(t.TempDir(), deps)
		if err != nil {
			t.Fatalf("openOverlay() error = %v", err)
		}
		defer store.Close()
		if !syncSeen {
			t.Fatal("metadata Set 未使用 Sync")
		}
	})

	t.Run("put", func(t *testing.T) {
		store, err := OpenOverlay(t.TempDir())
		if err != nil {
			t.Fatalf("OpenOverlay() error = %v", err)
		}
		defer store.Close()

		realDB := store.db
		syncSeen := false
		store.db = &testOverlayDB{
			overlayDB: realDB,
			set: func(key, value []byte, opts *pebble.WriteOptions) error {
				syncSeen = opts.GetSync()
				return realDB.Set(key, value, opts)
			},
		}
		if err := store.Put(netip.MustParseAddr("192.0.2.50"), Record{}, OverlayMeta{}); err != nil {
			t.Fatalf("Put() error = %v", err)
		}
		if !syncSeen {
			t.Fatal("Put Set 未使用 Sync")
		}
	})
}

func TestOverlaySerializesExpiredDeleteBeforeConcurrentPut(t *testing.T) {
	store, err := OpenOverlay(t.TempDir())
	if err != nil {
		t.Fatalf("OpenOverlay() error = %v", err)
	}
	defer store.Close()

	addr := netip.MustParseAddr("192.0.2.60")
	expiresAt := time.Date(2026, 7, 16, 0, 0, 0, 0, time.UTC)
	if err := store.Put(addr, Record{CountryCode: "OLD"}, OverlayMeta{ExpiresAt: expiresAt}); err != nil {
		t.Fatalf("initial Put() error = %v", err)
	}

	realDB := store.db
	deleteStarted := make(chan struct{})
	releaseDelete := make(chan struct{})
	store.db = &testOverlayDB{
		overlayDB: realDB,
		delete: func(key []byte, opts *pebble.WriteOptions) error {
			close(deleteStarted)
			<-releaseDelete
			return realDB.Delete(key, opts)
		},
		set: func(key, value []byte, opts *pebble.WriteOptions) error {
			return realDB.Set(key, value, opts)
		},
	}

	getErr := make(chan error, 1)
	go func() {
		_, _, matched, err := store.Get(addr, expiresAt)
		if err == nil && matched {
			err = errors.New("expired Get 意外命中")
		}
		getErr <- err
	}()
	<-deleteStarted

	putLockAttempted := make(chan struct{})
	store.beforeLock = func() {
		close(putLockAttempted)
	}
	putErr := make(chan error, 1)
	go func() {
		putErr <- store.Put(addr, Record{CountryCode: "NEW"}, OverlayMeta{})
	}()
	<-putLockAttempted
	if store.mu.TryLock() {
		store.mu.Unlock()
		t.Fatal("过期 Delete 执行期间未持有操作互斥锁")
	}

	close(releaseDelete)
	if err := <-getErr; err != nil {
		t.Fatalf("expired Get() error = %v", err)
	}
	if err := <-putErr; err != nil {
		t.Fatalf("concurrent Put() error = %v", err)
	}
	store.beforeLock = nil

	record, _, matched, err := store.Get(addr, time.Time{})
	if err != nil || !matched || record.CountryCode != "NEW" {
		t.Fatalf("final Get() = (%+v, %v, %v), want NEW hit", record, matched, err)
	}
}

func TestOverlayCloseWaitsForInFlightGet(t *testing.T) {
	store, err := OpenOverlay(t.TempDir())
	if err != nil {
		t.Fatalf("OpenOverlay() error = %v", err)
	}
	addr := netip.MustParseAddr("192.0.2.65")
	if err := store.Put(addr, Record{CountryCode: "US"}, OverlayMeta{}); err != nil {
		_ = store.Close()
		t.Fatalf("Put() error = %v", err)
	}

	realDB := store.db
	getStarted := make(chan struct{})
	releaseGet := make(chan struct{})
	closeCalled := make(chan struct{})
	store.db = &testOverlayDB{
		overlayDB: realDB,
		get: func(key []byte) ([]byte, io.Closer, error) {
			close(getStarted)
			<-releaseGet
			return realDB.Get(key)
		},
		close: func() error {
			close(closeCalled)
			return realDB.Close()
		},
	}

	getErr := make(chan error, 1)
	go func() {
		_, _, _, err := store.Get(addr, time.Time{})
		getErr <- err
	}()
	<-getStarted

	closeErr := make(chan error, 1)
	closeLockAttempted := make(chan struct{})
	store.beforeLock = func() {
		close(closeLockAttempted)
	}
	go func() {
		closeErr <- store.Close()
	}()
	<-closeLockAttempted
	if store.mu.TryLock() {
		store.mu.Unlock()
		t.Fatal("in-flight Get 执行期间未持有操作互斥锁")
	}

	close(releaseGet)
	if err := <-getErr; err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if err := <-closeErr; err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	<-closeCalled
}

func TestOverlayCloseWaitsForInFlightPut(t *testing.T) {
	store, err := OpenOverlay(t.TempDir())
	if err != nil {
		t.Fatalf("OpenOverlay() error = %v", err)
	}

	realDB := store.db
	setStarted := make(chan struct{})
	releaseSet := make(chan struct{})
	closeCalled := make(chan struct{})
	store.db = &testOverlayDB{
		overlayDB: realDB,
		set: func(key, value []byte, opts *pebble.WriteOptions) error {
			close(setStarted)
			<-releaseSet
			return realDB.Set(key, value, opts)
		},
		close: func() error {
			close(closeCalled)
			return realDB.Close()
		},
	}

	putErr := make(chan error, 1)
	go func() {
		putErr <- store.Put(netip.MustParseAddr("192.0.2.66"),
			Record{CountryCode: "US"}, OverlayMeta{})
	}()
	<-setStarted

	closeLockAttempted := make(chan struct{})
	store.beforeLock = func() {
		close(closeLockAttempted)
	}
	closeErr := make(chan error, 1)
	go func() {
		closeErr <- store.Close()
	}()
	<-closeLockAttempted
	if store.mu.TryLock() {
		store.mu.Unlock()
		t.Fatal("in-flight Put 执行期间未持有操作互斥锁")
	}

	close(releaseSet)
	if err := <-putErr; err != nil {
		t.Fatalf("Put() error = %v", err)
	}
	if err := <-closeErr; err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	<-closeCalled
}

func TestOverlayCloseOrderErrorsAndClosedMethods(t *testing.T) {
	dbErr := errors.New("db close failed")
	lockErr := errors.New("lock close failed")
	var events []string
	store := &OverlayStore{
		db: &testOverlayDB{
			close: func() error {
				events = append(events, "db")
				return dbErr
			},
		},
		lock: funcCloser(func() error {
			events = append(events, "lock")
			return lockErr
		}),
	}

	err := store.Close()
	if !errors.Is(err, dbErr) || !errors.Is(err, lockErr) {
		t.Fatalf("Close() error = %v, want joined db+lock errors", err)
	}
	if !strings.Contains(err.Error(), "关闭 overlay 数据库失败") ||
		!strings.Contains(err.Error(), "释放 overlay 生命周期锁失败") {
		t.Fatalf("Close() error 缺少资源来源上下文: %v", err)
	}
	if got := strings.Join(events, ","); got != "db,lock" {
		t.Fatalf("Close() order = %q, want db,lock", got)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
	addr := netip.MustParseAddr("192.0.2.70")
	if _, _, _, err := store.Get(addr, time.Time{}); !errors.Is(err, ErrOverlayClosed) {
		t.Fatalf("Get() error = %v, want ErrOverlayClosed", err)
	}
	if err := store.Put(addr, Record{}, OverlayMeta{}); !errors.Is(err, ErrOverlayClosed) {
		t.Fatalf("Put() error = %v, want ErrOverlayClosed", err)
	}

	var nilStore *OverlayStore
	if err := nilStore.Close(); err != nil {
		t.Fatalf("nil Close() error = %v", err)
	}
	if _, _, _, err := nilStore.Get(addr, time.Time{}); !errors.Is(err, ErrOverlayClosed) {
		t.Fatalf("nil Get() error = %v, want ErrOverlayClosed", err)
	}
	if err := nilStore.Put(addr, Record{}, OverlayMeta{}); !errors.Is(err, ErrOverlayClosed) {
		t.Fatalf("nil Put() error = %v, want ErrOverlayClosed", err)
	}
}

func TestBaseRebuildDoesNotTouchOpenOverlay(t *testing.T) {
	rootDir := t.TempDir()
	store, err := OpenOverlay(rootDir)
	if err != nil {
		t.Fatalf("OpenOverlay() error = %v", err)
	}
	addr := netip.MustParseAddr("203.0.113.10")
	wantRecord := Record{CountryCode: "EX", ASN: "AS64500"}
	if err := store.Put(addr, wantRecord, OverlayMeta{Source: "ipinfo"}); err != nil {
		_ = store.Close()
		t.Fatalf("Put() error = %v", err)
	}

	for i, network := range []string{"1.0.0.0/24", "2.0.0.0/24"} {
		csvPath := writeCSVFixture(t, t.TempDir(), strings.Join([]string{
			strings.Join(expectedCSVHeader, ","),
			network + ",A,A,A,A,,,",
		}, "\n"))
		if _, err := BuildFromCSV(rootDir, BuildOptions{
			CSVPath: csvPath,
			BuildID: fmt.Sprintf("overlay-isolation-%d", i),
		}); err != nil {
			_ = store.Close()
			t.Fatalf("BuildFromCSV(%d) error = %v", i, err)
		}
		record, _, matched, err := store.Get(addr, time.Time{})
		if err != nil || !matched || record.CountryCode != wantRecord.CountryCode {
			_ = store.Close()
			t.Fatalf("Get() after build %d = (%+v, %v, %v)", i, record, matched, err)
		}
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	reopened, err := OpenOverlay(rootDir)
	if err != nil {
		t.Fatalf("reopen OpenOverlay() error = %v", err)
	}
	defer reopened.Close()
	record, metadata, matched, err := reopened.Get(addr, time.Time{})
	if err != nil || !matched || record.CountryCode != wantRecord.CountryCode || metadata.Source != "ipinfo" {
		t.Fatalf("reopened Get() = (%+v, %+v, %v, %v)", record, metadata, matched, err)
	}
}

type testOverlayDB struct {
	overlayDB
	get    func([]byte) ([]byte, io.Closer, error)
	set    func([]byte, []byte, *pebble.WriteOptions) error
	delete func([]byte, *pebble.WriteOptions) error
	close  func() error
}

func (d *testOverlayDB) Get(key []byte) ([]byte, io.Closer, error) {
	if d.get != nil {
		return d.get(key)
	}
	return d.overlayDB.Get(key)
}

func (d *testOverlayDB) Set(key, value []byte, opts *pebble.WriteOptions) error {
	if d.set != nil {
		return d.set(key, value, opts)
	}
	return d.overlayDB.Set(key, value, opts)
}

func (d *testOverlayDB) Delete(key []byte, opts *pebble.WriteOptions) error {
	if d.delete != nil {
		return d.delete(key, opts)
	}
	return d.overlayDB.Delete(key, opts)
}

func (d *testOverlayDB) Close() error {
	if d.close != nil {
		return d.close()
	}
	return d.overlayDB.Close()
}

type closeErrorCloser struct {
	io.Closer
	err error
}

func (c *closeErrorCloser) Close() error {
	return errors.Join(c.Closer.Close(), c.err)
}

type funcCloser func() error

func (f funcCloser) Close() error {
	return f()
}

func mutatingCloser(value []byte) io.Closer {
	return funcCloser(func() error {
		clear(value)
		return nil
	})
}

func readOverlayDBValue(db overlayDB, key []byte) ([]byte, error) {
	value, closer, err := db.Get(key)
	if err != nil {
		return nil, err
	}
	owned := append([]byte(nil), value...)
	if err := closer.Close(); err != nil {
		return nil, err
	}
	return owned, nil
}

func writeRawOverlayRecord(
	t *testing.T,
	dbPath string,
	addr netip.Addr,
	record Record,
	metadata OverlayMeta,
) {
	t.Helper()
	key, err := encodeOverlayStoreKey(addr)
	if err != nil {
		t.Fatalf("encodeOverlayStoreKey() error = %v", err)
	}
	value, err := encodeOverlayRecordValueV1(record, metadata)
	if err != nil {
		t.Fatalf("encodeOverlayRecordValueV1() error = %v", err)
	}
	db, err := pebble.Open(dbPath, &pebble.Options{Logger: silentLogger{}})
	if err != nil {
		t.Fatalf("pebble.Open(%q) error = %v", dbPath, err)
	}
	if err := db.Set(key, value, pebble.Sync); err != nil {
		_ = db.Close()
		t.Fatalf("db.Set(evidence) error = %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("db.Close(evidence) error = %v", err)
	}
}

func assertRawOverlayEvidence(
	t *testing.T,
	dbPath string,
	metadataValue []byte,
	addr netip.Addr,
	wantRecord Record,
	wantMetadata OverlayMeta,
) {
	t.Helper()
	db, err := pebble.Open(dbPath, &pebble.Options{Logger: silentLogger{}})
	if err != nil {
		t.Fatalf("pebble.Open(evidence %q) error = %v", dbPath, err)
	}
	defer db.Close()

	gotMetadata, err := readOverlayDBValue(db, metadataKey)
	switch {
	case metadataValue == nil && !errors.Is(err, pebble.ErrNotFound):
		t.Fatalf("metadata error = %v, want ErrNotFound", err)
	case metadataValue != nil && err != nil:
		t.Fatalf("读取 evidence metadata 失败: %v", err)
	case metadataValue != nil && !bytes.Equal(gotMetadata, metadataValue):
		t.Fatalf("evidence metadata = %q, want %q", gotMetadata, metadataValue)
	}

	key, err := encodeOverlayStoreKey(addr)
	if err != nil {
		t.Fatalf("encodeOverlayStoreKey() error = %v", err)
	}
	value, err := readOverlayDBValue(db, key)
	if err != nil {
		t.Fatalf("读取 evidence record 失败: %v", err)
	}
	record, metadata, err := decodeOverlayRecordValueV1(value)
	if err != nil {
		t.Fatalf("decodeOverlayRecordValueV1() error = %v", err)
	}
	if record != wantRecord || metadata != wantMetadata {
		t.Fatalf("evidence = (%+v, %+v), want (%+v, %+v)",
			record, metadata, wantRecord, wantMetadata)
	}
}

func writeRawOverlayMetadata(t *testing.T, rootDir string, value []byte) {
	t.Helper()
	overlayDir := filepath.Join(rootDir, overlayDirName)
	if err := os.MkdirAll(overlayDir, 0755); err != nil {
		t.Fatalf("os.MkdirAll() error = %v", err)
	}
	db, err := pebble.Open(filepath.Join(overlayDir, dbDirName), &pebble.Options{Logger: silentLogger{}})
	if err != nil {
		t.Fatalf("pebble.Open() error = %v", err)
	}
	if value != nil {
		if err := db.Set(metadataKey, value, pebble.Sync); err != nil {
			_ = db.Close()
			t.Fatalf("db.Set(metadata) error = %v", err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatalf("db.Close() error = %v", err)
	}
}

func writeActiveOverlayMetadata(t *testing.T, rootDir string, value []byte) {
	t.Helper()
	db, err := pebble.Open(
		filepath.Join(rootDir, overlayDirName, dbDirName),
		&pebble.Options{Logger: silentLogger{}},
	)
	if err != nil {
		t.Fatalf("pebble.Open(active) error = %v", err)
	}
	if err := db.Set(metadataKey, value, pebble.Sync); err != nil {
		_ = db.Close()
		t.Fatalf("db.Set(active metadata) error = %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("db.Close(active) error = %v", err)
	}
}

func validOverlayMetadataJSON(t *testing.T) []byte {
	t.Helper()
	value, err := json.Marshal(OverlayMetadata{
		FormatVersion: overlayFormatVersion,
		CreatedAt:     time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC),
	})
	if err != nil {
		t.Fatalf("json.Marshal(valid metadata) error = %v", err)
	}
	return value
}

func overlayQuarantineDirs(t *testing.T, rootDir string) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(rootDir, overlayDirName, "quarantine-*"))
	if err != nil {
		t.Fatalf("filepath.Glob() error = %v", err)
	}
	return matches
}

func assertOverlayNotQuarantined(t *testing.T, rootDir string) {
	t.Helper()
	if got := len(overlayQuarantineDirs(t, rootDir)); got != 0 {
		t.Fatalf("quarantine count = %d, want 0", got)
	}
	if _, err := os.Stat(filepath.Join(rootDir, overlayDirName, dbDirName)); err != nil {
		t.Fatalf("active db 不应被移动: %v", err)
	}
}
