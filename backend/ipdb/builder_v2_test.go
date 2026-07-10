package ipdb

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"
)

// countV2Keys 直接打开 dbDir 扫描 Pebble，按 kind 字节统计 primary / cidr key 数；
// 顺带校验每个 cidr key 的 value 为零长度。
func countV2Keys(t *testing.T, dbDir string) (primary, cidr int) {
	t.Helper()

	db, err := pebble.Open(dbDir, &pebble.Options{Logger: silentLogger{}})
	if err != nil {
		t.Fatalf("pebble.Open(%q) error = %v", dbDir, err)
	}
	defer db.Close()

	iter, err := db.NewIter(nil)
	if err != nil {
		t.Fatalf("NewIter error = %v", err)
	}
	defer iter.Close()

	for valid := iter.First(); valid; valid = iter.Next() {
		key := iter.Key()
		if len(key) == 0 {
			continue
		}
		switch key[0] {
		case keyKindPrimaryV4, keyKindPrimaryV6:
			primary++
		case keyKindCIDRV4, keyKindCIDRV6:
			cidr++
			if len(iter.Value()) != 0 {
				t.Errorf("cidr key value 应为零长度，实际 %d 字节", len(iter.Value()))
			}
		}
	}
	return primary, cidr
}

func dbDirFor(rootDir, buildID string) string {
	return filepath.Join(rootDir, versionsDirName, buildID, dbDirName)
}

func TestBuildV2FromCSVDualIndex(t *testing.T) {
	rootDir := t.TempDir()
	csvPath := writeCSVFixture(t, t.TempDir(), strings.Join([]string{
		strings.Join(expectedCSVHeader, ","),
		`1.0.0.0/24,Australia,AU,Oceania,OC,AS13335,"Cloudflare, Inc.",cloudflare.com`,
		`1.0.1.0/24,China,CN,Asia,AS,,,`,
		`2001:db8::/48,Testland,TT,Test,TS,AS64500,Example IPv6,ipv6.example`,
		`2001:db8:1::/48,Testland,TT,Test,TS,,,`,
	}, "\n"))

	meta, err := buildV2FromCSV(rootDir, BuildOptions{CSVPath: csvPath, BuildID: "v2-build"})
	if err != nil {
		t.Fatalf("buildV2FromCSV() error = %v", err)
	}

	if meta.FormatVersion != int(formatVersionV2) {
		t.Fatalf("FormatVersion = %d, want %d", meta.FormatVersion, formatVersionV2)
	}
	if want := SchemaFeaturePrimaryLPM | SchemaFeatureCIDRStartIdx; meta.SchemaFeatures != want {
		t.Fatalf("SchemaFeatures = %d, want %d", meta.SchemaFeatures, want)
	}
	if meta.RowCount != 4 || meta.IPv4Count != 2 || meta.IPv6Count != 2 {
		t.Fatalf("counts mismatch: %#v", meta)
	}

	primary, cidr := countV2Keys(t, dbDirFor(rootDir, "v2-build"))
	if int64(primary) != meta.RowCount || int64(cidr) != meta.RowCount {
		t.Fatalf("primaryCount=%d cidrCount=%d, want both == RowCount=%d", primary, cidr, meta.RowCount)
	}
}

func TestBuildV2RejectsDuplicatePrefix(t *testing.T) {
	rootDir := t.TempDir()
	csvPath := writeCSVFixture(t, t.TempDir(), strings.Join([]string{
		strings.Join(expectedCSVHeader, ","),
		`10.0.0.0/8,A,A,A,A,,,`,
		`10.0.0.0/16,B,B,B,B,,,`,
		`10.0.0.0/8,C,C,C,C,,,`,
	}, "\n"))

	_, err := buildV2FromCSV(rootDir, BuildOptions{CSVPath: csvPath, BuildID: "dup-build"})
	if !errors.Is(err, ErrDuplicatePrefix) {
		t.Fatalf("error = %v, want ErrDuplicatePrefix", err)
	}
	if !strings.Contains(err.Error(), "10.0.0.0/8") ||
		!strings.Contains(err.Error(), "首次出现于第 2 行") ||
		!strings.Contains(err.Error(), "第 4 行") {
		t.Fatalf("error message missing canonical prefix / line numbers: %v", err)
	}
}

func TestBuildV2AllowsDistinctOverlap(t *testing.T) {
	rootDir := t.TempDir()
	csvPath := writeCSVFixture(t, t.TempDir(), strings.Join([]string{
		strings.Join(expectedCSVHeader, ","),
		`10.0.0.0/8,A,A,A,A,,,`,
		`10.0.0.0/16,B,B,B,B,,,`,
		`10.1.0.0/16,C,C,C,C,,,`,
	}, "\n"))

	meta, err := buildV2FromCSV(rootDir, BuildOptions{CSVPath: csvPath, BuildID: "overlap-build"})
	if err != nil {
		t.Fatalf("buildV2FromCSV() error = %v, want success (overlap allowed)", err)
	}
	if meta.RowCount != 3 {
		t.Fatalf("RowCount = %d, want 3", meta.RowCount)
	}

	primary, cidr := countV2Keys(t, dbDirFor(rootDir, "overlap-build"))
	if primary != 3 || cidr != 3 {
		t.Fatalf("primaryCount=%d cidrCount=%d, want 3/3", primary, cidr)
	}
}

func TestBuildV2DualWriteNotSplitByBatch(t *testing.T) {
	// 注入极小 commitSize，使 commit 切点落在行边界上，验证同一行的 primary+cidr
	// 不被拆到不同 batch 而出现计数不等 / 半写。
	orig := v2BatchCommitSize
	v2BatchCommitSize = 2
	defer func() { v2BatchCommitSize = orig }()

	rootDir := t.TempDir()
	csvPath := writeCSVFixture(t, t.TempDir(), strings.Join([]string{
		strings.Join(expectedCSVHeader, ","),
		`1.0.0.0/24,A,A,A,A,,,`,
		`1.0.1.0/24,B,B,B,B,,,`,
		`1.0.2.0/24,C,C,C,C,,,`,
		`2001:db8::/48,D,D,D,D,,,`,
		`2001:db8:1::/48,E,E,E,E,,,`,
	}, "\n"))

	meta, err := buildV2FromCSV(rootDir, BuildOptions{CSVPath: csvPath, BuildID: "batch-build"})
	if err != nil {
		t.Fatalf("buildV2FromCSV() error = %v", err)
	}

	primary, cidr := countV2Keys(t, dbDirFor(rootDir, "batch-build"))
	if int64(primary) != meta.RowCount || int64(cidr) != meta.RowCount {
		t.Fatalf("primaryCount=%d cidrCount=%d, want both == RowCount=%d", primary, cidr, meta.RowCount)
	}
}

func TestBuildV2StagingSuccess(t *testing.T) {
	rootDir := t.TempDir()
	csvPath := writeCSVFixture(t, t.TempDir(), strings.Join([]string{
		strings.Join(expectedCSVHeader, ","),
		`1.0.0.0/24,A,A,A,A,,,`,
		`1.0.1.0/24,B,B,B,B,,,`,
	}, "\n"))

	if _, err := buildV2FromCSV(rootDir, BuildOptions{CSVPath: csvPath, BuildID: "ok-build"}); err != nil {
		t.Fatalf("buildV2FromCSV() error = %v", err)
	}

	versionsDir := filepath.Join(rootDir, versionsDirName)
	if _, err := os.Stat(filepath.Join(versionsDir, "ok-build", dbDirName)); err != nil {
		t.Fatalf("正式目录缺失: %v", err)
	}
	assertNoStagingDirs(t, versionsDir)
	cur, err := os.ReadFile(filepath.Join(rootDir, currentFileName))
	if err != nil || string(cur) != "ok-build" {
		t.Fatalf("CURRENT = %q (err=%v), want ok-build", string(cur), err)
	}
}

func TestBuildV2StagingCleanupOnFailure(t *testing.T) {
	rootDir := t.TempDir()
	// 第二行 network 带 host bits（非规范网段），writeV2Records 在构建中途失败。
	csvPath := writeCSVFixture(t, t.TempDir(), strings.Join([]string{
		strings.Join(expectedCSVHeader, ","),
		`1.0.0.0/24,A,A,A,A,,,`,
		`10.0.0.1/24,B,B,B,B,,,`,
	}, "\n"))

	if _, err := buildV2FromCSV(rootDir, BuildOptions{CSVPath: csvPath, BuildID: "fail-build"}); err == nil {
		t.Fatal("buildV2FromCSV() error = nil, want failure on non-canonical network")
	}

	versionsDir := filepath.Join(rootDir, versionsDirName)
	if _, err := os.Stat(filepath.Join(versionsDir, "fail-build")); !os.IsNotExist(err) {
		t.Fatalf("失败构建不应留下正式目录，statErr = %v", err)
	}
	assertNoStagingDirs(t, versionsDir)
	if _, err := os.Stat(filepath.Join(rootDir, currentFileName)); !os.IsNotExist(err) {
		t.Fatalf("失败构建不应写 CURRENT，statErr = %v", err)
	}
}

func TestConcurrentBuildsKeepCurrentTarget(t *testing.T) {
	originalNow := buildIDNow
	buildIDNow = func() time.Time {
		return time.Date(2026, 7, 10, 1, 2, 3, 4, time.UTC)
	}
	t.Cleanup(func() { buildIDNow = originalNow })

	rootDir := t.TempDir()
	csvPath := writeCSVFixture(t, t.TempDir(), strings.Join([]string{
		strings.Join(expectedCSVHeader, ","),
		`1.0.0.0/24,A,A,A,A,,,`,
		`1.0.1.0/24,B,B,B,B,,,`,
	}, "\n"))

	const buildCount = 6
	start := make(chan struct{})
	errs := make(chan error, buildCount)
	var wg sync.WaitGroup

	for i := 0; i < buildCount; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := BuildFromCSV(rootDir, BuildOptions{CSVPath: csvPath})
			errs <- err
		}()
	}

	close(start)
	wg.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatalf("并发构建失败: %v", err)
		}
	}

	currentData, err := os.ReadFile(filepath.Join(rootDir, currentFileName))
	if err != nil {
		t.Fatalf("读取 CURRENT 失败: %v", err)
	}
	currentBuildID := string(currentData)
	if _, err := os.Stat(filepath.Join(rootDir, versionsDirName, currentBuildID, dbDirName)); err != nil {
		t.Fatalf("CURRENT=%q 指向的数据库不存在: %v", currentBuildID, err)
	}

	// BUILD.lock 串行化发布与回收；并发调用全部成功后只保留 CURRENT 指向的版本。
	versionsDir := filepath.Join(rootDir, versionsDirName)
	assertOnlyVersion(t, versionsDir, currentBuildID)
	assertNoStagingDirs(t, versionsDir)

	store, err := OpenCurrent(rootDir)
	if err != nil {
		t.Fatalf("并发发布后 OpenCurrent() 失败: %v", err)
	}
	defer store.Close()
	match, err := store.LookupIP("1.0.0.1")
	if err != nil || !match.Matched {
		t.Fatalf("并发发布后的当前库不可查询: match=%+v err=%v", match, err)
	}
}

func TestConcurrentBuildsAcrossProcessesKeepCurrentTarget(t *testing.T) {
	const helperEnv = "GEOPRISM_IPDB_BUILD_HELPER"
	if os.Getenv(helperEnv) == "1" {
		rootDir := os.Getenv("GEOPRISM_IPDB_ROOT")
		csvPath := os.Getenv("GEOPRISM_IPDB_CSV")
		processID := os.Getenv("GEOPRISM_IPDB_PROCESS_ID")
		startFile := os.Getenv("GEOPRISM_IPDB_START_FILE")
		readyDir := os.Getenv("GEOPRISM_IPDB_READY_DIR")
		if err := os.WriteFile(filepath.Join(readyDir, processID), []byte("ready"), 0644); err != nil {
			t.Fatalf("写入子进程 ready 标记失败: %v", err)
		}

		deadline := time.Now().Add(10 * time.Second)
		for {
			if _, err := os.Stat(startFile); err == nil {
				break
			} else if !os.IsNotExist(err) {
				t.Fatalf("检查并发启动屏障失败: %v", err)
			}
			if time.Now().After(deadline) {
				t.Fatal("等待并发启动屏障超时")
			}
			time.Sleep(5 * time.Millisecond)
		}

		if _, err := BuildFromCSV(rootDir, BuildOptions{CSVPath: csvPath}); err != nil {
			t.Fatalf("子进程构建 %s 失败: %v", processID, err)
		}
		return
	}

	rootDir := t.TempDir()
	csvPath := writeCSVFixture(t, t.TempDir(), strings.Join([]string{
		strings.Join(expectedCSVHeader, ","),
		`1.0.0.0/24,A,A,A,A,,,`,
		`1.0.1.0/24,B,B,B,B,,,`,
	}, "\n"))
	startFile := filepath.Join(rootDir, "start-builds")
	readyDir := filepath.Join(rootDir, "ready")
	if err := os.MkdirAll(readyDir, 0755); err != nil {
		t.Fatalf("创建 ready 目录失败: %v", err)
	}

	type childProcess struct {
		cmd    *exec.Cmd
		output bytes.Buffer
	}
	const processCount = 4
	children := make([]childProcess, processCount)
	for i := 0; i < processCount; i++ {
		processID := fmt.Sprintf("process-%d", i)
		cmd := exec.Command(os.Args[0], "-test.run=^TestConcurrentBuildsAcrossProcessesKeepCurrentTarget$", "-test.count=1")
		cmd.Env = append(os.Environ(),
			helperEnv+"=1",
			"GEOPRISM_IPDB_ROOT="+rootDir,
			"GEOPRISM_IPDB_CSV="+csvPath,
			"GEOPRISM_IPDB_PROCESS_ID="+processID,
			"GEOPRISM_IPDB_START_FILE="+startFile,
			"GEOPRISM_IPDB_READY_DIR="+readyDir,
		)
		children[i].cmd = cmd
		cmd.Stdout = &children[i].output
		cmd.Stderr = &children[i].output
		if err := cmd.Start(); err != nil {
			t.Fatalf("启动构建子进程 %d 失败: %v", i, err)
		}
		child := &children[i]
		t.Cleanup(func() {
			if child.cmd.Process != nil && child.cmd.ProcessState == nil {
				_ = child.cmd.Process.Kill()
				_ = child.cmd.Wait()
			}
		})
	}

	readyDeadline := time.Now().Add(10 * time.Second)
	for {
		entries, err := os.ReadDir(readyDir)
		if err != nil {
			t.Fatalf("读取 ready 目录失败: %v", err)
		}
		if len(entries) == processCount {
			break
		}
		if time.Now().After(readyDeadline) {
			t.Fatalf("等待构建子进程就绪超时: ready=%d/%d", len(entries), processCount)
		}
		time.Sleep(5 * time.Millisecond)
	}

	if err := os.WriteFile(startFile, []byte("start"), 0644); err != nil {
		t.Fatalf("释放并发启动屏障失败: %v", err)
	}
	for i := range children {
		if err := children[i].cmd.Wait(); err != nil {
			t.Fatalf("构建子进程 %d 失败: %v\n%s", i, err, children[i].output.String())
		}
	}

	currentData, err := os.ReadFile(filepath.Join(rootDir, currentFileName))
	if err != nil {
		t.Fatalf("读取 CURRENT 失败: %v", err)
	}
	currentBuildID := string(currentData)
	versionsDir := filepath.Join(rootDir, versionsDirName)
	assertOnlyVersion(t, versionsDir, currentBuildID)
	assertNoStagingDirs(t, versionsDir)

	store, err := OpenCurrent(rootDir)
	if err != nil {
		t.Fatalf("跨进程并发发布后 OpenCurrent() 失败: %v", err)
	}
	defer store.Close()
	match, err := store.LookupIP("1.0.0.1")
	if err != nil || !match.Matched {
		t.Fatalf("跨进程并发发布后的当前库不可查询: match=%+v err=%v", match, err)
	}
}

func TestDefaultBuildIDReallocatesExistingName(t *testing.T) {
	fixedNow := time.Date(2026, 7, 10, 1, 2, 3, 4, time.UTC)
	originalNow := buildIDNow
	buildIDNow = func() time.Time { return fixedNow }
	t.Cleanup(func() { buildIDNow = originalNow })

	rootDir := t.TempDir()
	csvPath := writeCSVFixture(t, t.TempDir(), strings.Join([]string{
		strings.Join(expectedCSVHeader, ","),
		`1.0.0.0/24,A,A,A,A,,,`,
	}, "\n"))
	baseID := fixedNow.Format("20060102T150405.000000000")
	if _, err := BuildFromCSV(rootDir, BuildOptions{CSVPath: csvPath, BuildID: baseID}); err != nil {
		t.Fatalf("构造默认 ID 同名正式版本失败: %v", err)
	}
	if _, err := BuildFromCSV(rootDir, BuildOptions{CSVPath: csvPath}); err != nil {
		t.Fatalf("空 BuildID 遇到同名正式版本后应重新分配: %v", err)
	}

	currentData, err := os.ReadFile(filepath.Join(rootDir, currentFileName))
	if err != nil {
		t.Fatalf("读取 CURRENT 失败: %v", err)
	}
	want := baseID + "-1"
	if got := string(currentData); got != want {
		t.Fatalf("CURRENT=%q, want %q", got, want)
	}
	assertOnlyVersion(t, filepath.Join(rootDir, versionsDirName), want)
}

func TestBuildLockReleasedAfterProcessExit(t *testing.T) {
	const helperEnv = "GEOPRISM_IPDB_LOCK_HOLDER"
	if os.Getenv(helperEnv) == "1" {
		rootDir := os.Getenv("GEOPRISM_IPDB_ROOT")
		readyFile := os.Getenv("GEOPRISM_IPDB_READY_FILE")
		versionsDir := filepath.Join(rootDir, versionsDirName)
		if err := os.MkdirAll(versionsDir, 0755); err != nil {
			t.Fatalf("创建 helper versions 目录失败: %v", err)
		}
		lock, err := acquireFileLock(filepath.Join(rootDir, buildLockFileName), true)
		if err != nil {
			t.Fatalf("helper 获取 BUILD.lock 失败: %v", err)
		}
		defer lock.Close()
		if err := os.MkdirAll(filepath.Join(versionsDir, stagingDirPrefix+"killed", dbDirName), 0755); err != nil {
			t.Fatalf("helper 构造 crash staging 失败: %v", err)
		}
		if err := os.WriteFile(readyFile, []byte("ready"), 0644); err != nil {
			t.Fatalf("helper 写 ready 标记失败: %v", err)
		}
		time.Sleep(time.Hour)
		return
	}

	rootDir := t.TempDir()
	readyFile := filepath.Join(rootDir, "lock-holder-ready")
	cmd := exec.Command(os.Args[0], "-test.run=^TestBuildLockReleasedAfterProcessExit$", "-test.count=1")
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	cmd.Env = append(os.Environ(),
		helperEnv+"=1",
		"GEOPRISM_IPDB_ROOT="+rootDir,
		"GEOPRISM_IPDB_READY_FILE="+readyFile,
	)
	if err := cmd.Start(); err != nil {
		t.Fatalf("启动 lock holder 失败: %v", err)
	}
	t.Cleanup(func() {
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
			t.Fatalf("检查 lock holder ready 标记失败: %v", err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("等待 lock holder 就绪超时\n%s", output.String())
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("终止 lock holder 失败: %v", err)
	}
	_ = cmd.Wait()

	csvPath := writeCSVFixture(t, t.TempDir(), strings.Join([]string{
		strings.Join(expectedCSVHeader, ","),
		`1.0.0.0/24,A,A,A,A,,,`,
	}, "\n"))
	if _, err := buildV2FromCSV(rootDir, BuildOptions{CSVPath: csvPath, BuildID: "after-kill"}); err != nil {
		t.Fatalf("持锁进程退出后的构建失败: %v", err)
	}
	versionsDir := filepath.Join(rootDir, versionsDirName)
	assertOnlyVersion(t, versionsDir, "after-kill")
	assertNoStagingDirs(t, versionsDir)
}

func TestBuildV2RetentionAndCrashStagingRecovery(t *testing.T) {
	rootDir := t.TempDir()
	csvPath := writeCSVFixture(t, t.TempDir(), strings.Join([]string{
		strings.Join(expectedCSVHeader, ","),
		`1.0.0.0/24,A,A,A,A,,,`,
	}, "\n"))

	if _, err := buildV2FromCSV(rootDir, BuildOptions{CSVPath: csvPath, BuildID: "first"}); err != nil {
		t.Fatalf("首次构建失败: %v", err)
	}
	crashStaging := filepath.Join(rootDir, versionsDirName, stagingDirPrefix+"crashed", dbDirName)
	if err := os.MkdirAll(crashStaging, 0755); err != nil {
		t.Fatalf("构造 crash staging 失败: %v", err)
	}

	if _, err := buildV2FromCSV(rootDir, BuildOptions{CSVPath: csvPath, BuildID: "second"}); err != nil {
		t.Fatalf("第二次构建失败: %v", err)
	}

	versionsDir := filepath.Join(rootDir, versionsDirName)
	assertOnlyVersion(t, versionsDir, "second")
	assertNoStagingDirs(t, versionsDir)
	if _, err := os.Stat(filepath.Join(versionsDir, "first")); !os.IsNotExist(err) {
		t.Fatalf("旧正式版本应被回收，statErr=%v", err)
	}
}

func TestCrashStagingCleanupPreservesCurrentWithReservedPrefix(t *testing.T) {
	rootDir := t.TempDir()
	validCSV := writeCSVFixture(t, t.TempDir(), strings.Join([]string{
		strings.Join(expectedCSVHeader, ","),
		`1.0.0.0/24,A,A,A,A,,,`,
	}, "\n"))
	if _, err := buildV2FromCSV(rootDir, BuildOptions{
		CSVPath: validCSV,
		BuildID: stagingDirPrefix + "prod",
	}); err != nil {
		t.Fatalf("构建保留前缀正式版本失败: %v", err)
	}

	invalidCSV := writeCSVFixture(t, t.TempDir(), strings.Join([]string{
		strings.Join(expectedCSVHeader, ","),
		`10.0.0.1/24,B,B,B,B,,,`,
	}, "\n"))
	if _, err := buildV2FromCSV(rootDir, BuildOptions{
		CSVPath: invalidCSV,
		BuildID: "expected-failure",
	}); err == nil {
		t.Fatal("后续非法构建应失败")
	}

	currentData, err := os.ReadFile(filepath.Join(rootDir, currentFileName))
	if err != nil || string(currentData) != stagingDirPrefix+"prod" {
		t.Fatalf("失败构建后 CURRENT=%q err=%v", string(currentData), err)
	}
	store, err := OpenCurrent(rootDir)
	if err != nil {
		t.Fatalf("失败构建后当前正式版本不可打开: %v", err)
	}
	defer store.Close()
	match, err := store.LookupIP("1.0.0.1")
	if err != nil || !match.Matched {
		t.Fatalf("失败构建后当前正式版本不可查询: match=%+v err=%v", match, err)
	}
}

func TestCleanupWaitsForCurrentReader(t *testing.T) {
	rootDir := t.TempDir()
	csvPath := writeCSVFixture(t, t.TempDir(), strings.Join([]string{
		strings.Join(expectedCSVHeader, ","),
		`1.0.0.0/24,A,A,A,A,,,`,
	}, "\n"))
	if _, err := buildV2FromCSV(rootDir, BuildOptions{CSVPath: csvPath, BuildID: "reader-old"}); err != nil {
		t.Fatalf("构建 reader-old 失败: %v", err)
	}

	store, err := OpenCurrent(rootDir)
	if err != nil {
		t.Fatalf("OpenCurrent() 失败: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	versionsDir := filepath.Join(rootDir, versionsDirName)
	buildDone := make(chan error, 1)
	go func() {
		_, err := buildV2FromCSV(rootDir, BuildOptions{CSVPath: csvPath, BuildID: "reader-new"})
		buildDone <- err
	}()

	// 等到第二次构建已写完并关闭 staging DB。此时它只能阻塞在 VERSIONS.lock
	// 独占获取上，尚不能切换 CURRENT 或清理 reader-old。
	waitForClosedStagingDB(t, versionsDir, buildDone)

	select {
	case err := <-buildDone:
		t.Fatalf("reader 未关闭时第二次构建不应完成，err=%v", err)
	case <-time.After(100 * time.Millisecond):
	}
	currentData, err := os.ReadFile(filepath.Join(rootDir, currentFileName))
	if err != nil || string(currentData) != "reader-old" {
		t.Fatalf("reader 存活期间 CURRENT=%q err=%v, want reader-old", string(currentData), err)
	}

	match, err := store.LookupIP("1.0.0.1")
	if err != nil || !match.Matched {
		t.Fatalf("等待清理期间旧 reader 应保持可查询: match=%+v err=%v", match, err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("关闭旧 reader 失败: %v", err)
	}

	select {
	case err := <-buildDone:
		if err != nil {
			t.Fatalf("reader 关闭后第二次构建失败: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("reader 关闭后第二次构建仍未完成")
	}
	if _, err := os.Stat(filepath.Join(versionsDir, "reader-old")); !os.IsNotExist(err) {
		t.Fatalf("reader 关闭后旧版本应被回收，statErr=%v", err)
	}
	assertOnlyVersion(t, versionsDir, "reader-new")
}

func waitForClosedStagingDB(t *testing.T, versionsDir string, buildDone <-chan error) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-buildDone:
			t.Fatalf("staging 就绪前构建已结束: %v", err)
		default:
		}

		entries, err := os.ReadDir(versionsDir)
		if err != nil {
			t.Fatalf("读取 versions 目录失败: %v", err)
		}
		for _, entry := range entries {
			if !entry.IsDir() || !strings.HasPrefix(entry.Name(), stagingDirPrefix) {
				continue
			}
			dbDir := filepath.Join(versionsDir, entry.Name(), dbDirName)
			db, err := pebble.Open(dbDir, &pebble.Options{ReadOnly: true, Logger: silentLogger{}})
			if err != nil {
				continue
			}
			_, closer, metaErr := db.Get(metadataKey)
			if metaErr == nil {
				metaErr = closer.Close()
			}
			closeErr := db.Close()
			if metaErr == nil && closeErr == nil {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("等待已关闭的 staging DB 超时")
}

func assertNoStagingDirs(t *testing.T, versionsDir string) {
	t.Helper()
	entries, err := os.ReadDir(versionsDir)
	if err != nil {
		t.Fatalf("读取 versions 目录失败: %v", err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), stagingDirPrefix) {
			t.Fatalf("构建结束后残留 staging 目录: %s", entry.Name())
		}
	}
}

func assertOnlyVersion(t *testing.T, versionsDir, want string) {
	t.Helper()
	entries, err := os.ReadDir(versionsDir)
	if err != nil {
		t.Fatalf("读取 versions 目录失败: %v", err)
	}
	var versions []string
	for _, entry := range entries {
		if entry.IsDir() && !strings.HasPrefix(entry.Name(), stagingDirPrefix) {
			versions = append(versions, entry.Name())
		}
	}
	if len(versions) != 1 || versions[0] != want {
		t.Fatalf("正式版本目录=%v, want [%s]", versions, want)
	}
}

// collectPrimaryPrefixes 扫库收集所有 primary key 解出的 prefix 字符串。
func collectPrimaryPrefixes(t *testing.T, dbDir string) []string {
	t.Helper()
	db, err := pebble.Open(dbDir, &pebble.Options{Logger: silentLogger{}})
	if err != nil {
		t.Fatalf("pebble.Open(%q) error = %v", dbDir, err)
	}
	defer db.Close()

	iter, err := db.NewIter(nil)
	if err != nil {
		t.Fatalf("NewIter error = %v", err)
	}
	defer iter.Close()

	var out []string
	for valid := iter.First(); valid; valid = iter.Next() {
		key := iter.Key()
		if len(key) == 0 {
			continue
		}
		if key[0] == keyKindPrimaryV4 || key[0] == keyKindPrimaryV6 {
			p, err := decodePrimaryKeyV2(key)
			if err != nil {
				t.Fatalf("decodePrimaryKeyV2 error = %v", err)
			}
			out = append(out, p.String())
		}
	}
	return out
}

func TestBuildV2RejectsOutOfOrder(t *testing.T) {
	rootDir := t.TempDir()
	// 同 family 内起始地址递减 → 乱序。
	csvPath := writeCSVFixture(t, t.TempDir(), strings.Join([]string{
		strings.Join(expectedCSVHeader, ","),
		`2.0.0.0/24,A,A,A,A,,,`,
		`1.0.0.0/24,B,B,B,B,,,`,
	}, "\n"))

	_, err := buildV2FromCSV(rootDir, BuildOptions{CSVPath: csvPath, BuildID: "order-build"})
	if err == nil {
		t.Fatal("buildV2FromCSV() error = nil, want out-of-order error")
	}
	if !strings.Contains(err.Error(), "乱序网段") {
		t.Fatalf("error = %v, want 乱序网段", err)
	}
	if _, statErr := os.Stat(filepath.Join(rootDir, currentFileName)); !os.IsNotExist(statErr) {
		t.Fatalf("乱序失败不应写 CURRENT，statErr = %v", statErr)
	}
}

func TestBuildV2SingleIPRows(t *testing.T) {
	rootDir := t.TempDir()
	csvPath := writeCSVFixture(t, t.TempDir(), strings.Join([]string{
		strings.Join(expectedCSVHeader, ","),
		`1.7.168.172,India,IN,Asia,AS,AS9583,Sify Limited,sifycorp.com`,
		`2001:db8::1,Testland,TT,Test,TS,,,`,
	}, "\n"))

	meta, err := buildV2FromCSV(rootDir, BuildOptions{CSVPath: csvPath, BuildID: "singleip-build"})
	if err != nil {
		t.Fatalf("buildV2FromCSV() error = %v", err)
	}
	if meta.RowCount != 2 || meta.IPv4Count != 1 || meta.IPv6Count != 1 {
		t.Fatalf("counts mismatch: %#v", meta)
	}

	prefixes := collectPrimaryPrefixes(t, dbDirFor(rootDir, "singleip-build"))
	if !slices.Contains(prefixes, "1.7.168.172/32") || !slices.Contains(prefixes, "2001:db8::1/128") {
		t.Fatalf("primary prefixes = %v, want 含 /32 与 /128", prefixes)
	}
}

func TestBuildV2BoundaryPrefixes(t *testing.T) {
	rootDir := t.TempDir()
	// 各 family 内起始地址非递减；覆盖 /0、/32、/128。
	csvPath := writeCSVFixture(t, t.TempDir(), strings.Join([]string{
		strings.Join(expectedCSVHeader, ","),
		`0.0.0.0/0,A,A,A,A,,,`,
		`255.255.255.255/32,B,B,B,B,,,`,
		`::/0,C,C,C,C,,,`,
		`2001:db8::1/128,D,D,D,D,,,`,
	}, "\n"))

	meta, err := buildV2FromCSV(rootDir, BuildOptions{CSVPath: csvPath, BuildID: "boundary-build"})
	if err != nil {
		t.Fatalf("buildV2FromCSV() error = %v", err)
	}
	primary, cidr := countV2Keys(t, dbDirFor(rootDir, "boundary-build"))
	if int64(primary) != meta.RowCount || int64(cidr) != meta.RowCount {
		t.Fatalf("primaryCount=%d cidrCount=%d, want both == %d", primary, cidr, meta.RowCount)
	}
	prefixes := collectPrimaryPrefixes(t, dbDirFor(rootDir, "boundary-build"))
	for _, want := range []string{"0.0.0.0/0", "255.255.255.255/32", "::/0", "2001:db8::1/128"} {
		if !slices.Contains(prefixes, want) {
			t.Fatalf("primary prefixes = %v, missing %s", prefixes, want)
		}
	}
}

func TestBuildV2PrimaryCidrSameSource(t *testing.T) {
	rootDir := t.TempDir()
	csvPath := writeCSVFixture(t, t.TempDir(), strings.Join([]string{
		strings.Join(expectedCSVHeader, ","),
		`1.0.0.0/24,Australia,AU,Oceania,OC,AS13335,"Cloudflare, Inc.",cloudflare.com`,
	}, "\n"))
	if _, err := buildV2FromCSV(rootDir, BuildOptions{CSVPath: csvPath, BuildID: "src-build"}); err != nil {
		t.Fatalf("buildV2FromCSV() error = %v", err)
	}

	db, err := pebble.Open(dbDirFor(rootDir, "src-build"), &pebble.Options{Logger: silentLogger{}})
	if err != nil {
		t.Fatalf("pebble.Open error = %v", err)
	}
	defer db.Close()

	iter, err := db.NewIter(nil)
	if err != nil {
		t.Fatalf("NewIter error = %v", err)
	}
	defer iter.Close()

	checked := 0
	for valid := iter.First(); valid; valid = iter.Next() {
		key := iter.Key()
		if len(key) == 0 || (key[0] != keyKindPrimaryV4 && key[0] != keyKindPrimaryV6) {
			continue
		}

		primaryPrefix, err := decodePrimaryKeyV2(key)
		if err != nil {
			t.Fatalf("decodePrimaryKeyV2 error = %v", err)
		}

		// 对应 cidr key 必须存在且 decode 出同一 prefix（同源不变量）。
		cidrKey, err := encodeCIDRKeyV2(primaryPrefix)
		if err != nil {
			t.Fatalf("encodeCIDRKeyV2 error = %v", err)
		}
		cidrVal, closer, err := db.Get(cidrKey)
		if err != nil {
			t.Fatalf("对应 cidr key 缺失: %v", err)
		}
		if len(cidrVal) != 0 {
			t.Fatalf("cidr value 应为零长度，实际 %d", len(cidrVal))
		}
		closer.Close()

		cidrPrefix, err := decodeCIDRKeyV2(cidrKey)
		if err != nil {
			t.Fatalf("decodeCIDRKeyV2 error = %v", err)
		}
		if cidrPrefix != primaryPrefix {
			t.Fatalf("cidr/primary 还原不一致: %v vs %v", cidrPrefix, primaryPrefix)
		}

		// primary value 解出正确业务字段，且 Network 为空（待 query 回填）。
		rec, err := decodeBaseRecordValueV2(iter.Value())
		if err != nil {
			t.Fatalf("decodeBaseRecordValueV2 error = %v", err)
		}
		if rec.Network != "" {
			t.Fatalf("decode 返回的 Network 应为空，实际 %q", rec.Network)
		}
		if rec.Country != "Australia" || rec.ASN != "AS13335" || rec.ASDomain != "cloudflare.com" {
			t.Fatalf("业务字段解码错误: %#v", rec)
		}
		checked++
	}
	if checked != 1 {
		t.Fatalf("应校验 1 条 primary 记录，实际 %d", checked)
	}
}

func TestBuildV2RejectsIPv4MappedIPv6(t *testing.T) {
	rootDir := t.TempDir()
	// ::ffff:1.2.3.4 → IPv4-mapped IPv6，v1 parse 合法但 v2 encode 拒绝（family 二义）。
	csvPath := writeCSVFixture(t, t.TempDir(), strings.Join([]string{
		strings.Join(expectedCSVHeader, ","),
		`::ffff:1.2.3.4,A,A,A,A,,,`,
	}, "\n"))

	_, err := buildV2FromCSV(rootDir, BuildOptions{CSVPath: csvPath, BuildID: "mapped-build"})
	if err == nil {
		t.Fatal("buildV2FromCSV() error = nil, want IPv4-mapped IPv6 rejection")
	}
	if !strings.Contains(err.Error(), "IPv4-mapped") {
		t.Fatalf("error = %v, want IPv4-mapped 拒绝", err)
	}
	if _, statErr := os.Stat(filepath.Join(rootDir, versionsDirName, "mapped-build")); !os.IsNotExist(statErr) {
		t.Fatalf("失败不应留正式目录，statErr = %v", statErr)
	}
}

func TestBuildV2RejectsBadInputs(t *testing.T) {
	t.Run("相对路径", func(t *testing.T) {
		_, err := buildV2FromCSV(t.TempDir(), BuildOptions{CSVPath: "relative.csv"})
		if err == nil || !strings.Contains(err.Error(), "绝对路径") {
			t.Fatalf("error = %v, want 绝对路径", err)
		}
	})

	t.Run("表头不符", func(t *testing.T) {
		csvPath := writeCSVFixture(t, t.TempDir(), strings.Join([]string{
			"network,wrong,header,cols,x,y,z,w",
			`1.0.0.0/24,A,A,A,A,,,`,
		}, "\n"))
		_, err := buildV2FromCSV(t.TempDir(), BuildOptions{CSVPath: csvPath, BuildID: "badhdr"})
		if err == nil || !strings.Contains(err.Error(), "CSV 头不匹配") {
			t.Fatalf("error = %v, want CSV 头不匹配", err)
		}
	})

	t.Run("非规范网段带host位", func(t *testing.T) {
		csvPath := writeCSVFixture(t, t.TempDir(), strings.Join([]string{
			strings.Join(expectedCSVHeader, ","),
			`10.0.0.1/24,A,A,A,A,,,`,
		}, "\n"))
		_, err := buildV2FromCSV(t.TempDir(), BuildOptions{CSVPath: csvPath, BuildID: "hostbits"})
		if err == nil || !strings.Contains(err.Error(), "不是规范网段") {
			t.Fatalf("error = %v, want 不是规范网段", err)
		}
	})
}
