package ipdb

import (
	"bytes"
	"math/rand"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// 本文件放 LookupIP/LookupCIDR 的 property test（roadmap §7 硬性验收项）：
// 随机 prefix 集 + 暴力 oracle，验证真 LPM / 三段 CIDR 与暴力结果一致，防算法退化。
// 覆盖边界：/0、/32、/128、同起始不同 prefixLen、多父覆盖。

// randPrefix 生成一个随机 masked prefix（V4 或 V6）。
func randPrefix(rng *rand.Rand) netip.Prefix {
	var addr netip.Addr
	var maxBits int
	if rng.Intn(2) == 0 {
		var raw [4]byte
		rng.Read(raw[:])
		addr = netip.AddrFrom4(raw)
		maxBits = 32
	} else {
		var raw [16]byte
		rng.Read(raw[:])
		addr = netip.AddrFrom16(raw)
		maxBits = 128
	}
	// prefixLen 在 [0, maxBits] 内随机；偏向较小值以增加重叠概率（更能压测 LPM/CIDR）。
	bits := rng.Intn(maxBits + 1)
	return netip.PrefixFrom(addr, bits).Masked()
}

// bruteForceLookupIP 暴力 oracle：遍历所有 prefix，返回包含 addr 的最长前缀。
// 无命中返回 (Prefix{}, false)。
func bruteForceLookupIP(prefixes []netip.Prefix, addr netip.Addr) (netip.Prefix, bool) {
	var best netip.Prefix
	found := false
	for _, p := range prefixes {
		if p.Contains(addr) {
			if !found || p.Bits() > best.Bits() {
				best = p
				found = true
			}
		}
	}
	return best, found
}

// bruteForceLookupCIDR 暴力 oracle：返回所有与 query 相交的 prefix，
// 按 encodeCIDRKeyV2 字节序排序（与 LookupCIDR 实现一致）。
func bruteForceLookupCIDR(prefixes []netip.Prefix, query netip.Prefix) []netip.Prefix {
	query = query.Masked()
	var hits []netip.Prefix
	for _, p := range prefixes {
		if prefixesIntersect(p, query) {
			hits = append(hits, p)
		}
	}
	sort.Slice(hits, func(i, j int) bool {
		ki, _ := encodeCIDRKeyV2(hits[i])
		kj, _ := encodeCIDRKeyV2(hits[j])
		return bytes.Compare(ki, kj) < 0
	})
	return hits
}

// prefixesIntersect 判断两个同 family prefix 是否相交（含包含/被包含/部分重叠）。
func prefixesIntersect(a, b netip.Prefix) bool {
	a = a.Masked()
	b = b.Masked()
	if a.Addr().BitLen() != b.Addr().BitLen() {
		return false
	}
	return a.Contains(b.Addr()) || b.Contains(a.Addr())
}

// buildPrefixFixture 把一组 prefix 构建成 v2 库 CSV 并构建，返回 rootDir。
// 同一 prefix 重复时只保留首次（避免 buildV2FromCSV 的 duplicate-prefix reject）。
// 排序：v2 builder 要求每个 family 内起始地址非递减，故按 (family, addrBytes) 排序后再写 CSV。
func buildPrefixFixture(t *testing.T, buildID string, prefixes []netip.Prefix) string {
	t.Helper()
	seen := map[string]bool{}
	unique := make([]netip.Prefix, 0, len(prefixes))
	for _, p := range prefixes {
		s := p.String()
		if seen[s] {
			continue
		}
		seen[s] = true
		unique = append(unique, p)
	}
	// 按 (family, addrBytes) 排序，满足 v2 builder 的"family 内起始地址非递减"输入契约。
	sort.Slice(unique, func(i, j int) bool {
		fi, ai := familyAndAddrBytes(unique[i])
		fj, aj := familyAndAddrBytes(unique[j])
		if fi != fj {
			return fi < fj
		}
		return bytes.Compare(ai, aj) < 0
	})

	var lines []string
	lines = append(lines, strings.Join(expectedCSVHeader, ","))
	for _, p := range unique {
		// 业务字段填占位值（property test 只关心 Network 与命中集合）。
		lines = append(lines, p.String()+",X,X,X,X,AS,AS,as.example")
	}
	rootDir := t.TempDir()
	csvPath := filepath.Join(t.TempDir(), "prop.csv")
	if err := os.WriteFile(csvPath, []byte(strings.Join(lines, "\n")), 0644); err != nil {
		t.Fatalf("write CSV error = %v", err)
	}
	if _, err := buildV2FromCSV(rootDir, BuildOptions{CSVPath: csvPath, BuildID: buildID}); err != nil {
		t.Fatalf("buildV2FromCSV error = %v", err)
	}
	return rootDir
}

// familyAndAddrBytes 返回 prefix 的 family 字节（V4=0,V6=1，仅用于排序分组）与纯地址字节。
func familyAndAddrBytes(p netip.Prefix) (byte, []byte) {
	if p.Addr().Is4() {
		a := p.Addr().As4()
		return 0, a[:]
	}
	a := p.Addr().As16()
	return 1, a[:]
}

// TestLookupPropertyLPM property test：随机 prefix 集下 LookupIP 与暴力 oracle 一致。
func TestLookupPropertyLPM(t *testing.T) {
	rng := rand.New(rand.NewSource(42)) // 固定种子，可复现

	const rounds = 200 // 每轮一个独立库
	for round := 0; round < rounds; round++ {
		// 每轮 5-15 个随机 prefix（含可能的重复，buildPrefixFixture 去重）。
		n := 5 + rng.Intn(11)
		raw := make([]netip.Prefix, n)
		for i := range raw {
			raw[i] = randPrefix(rng)
		}
		prefixes := dedupPrefixes(raw)

		rootDir := buildPrefixFixture(t, "prop-lpm", prefixes)
		store := openV2Fixture(t, rootDir, "prop-lpm")

		// 每轮查 10 个随机 IP（覆盖库内 prefix 的 addr + 完全随机 addr）。
		for q := 0; q < 10; q++ {
			var addr netip.Addr
			if q < len(prefixes) {
				// 用库内某 prefix 的随机地址（大概率命中）。
				addr = randomAddrInPrefix(rng, prefixes[q%len(prefixes)])
			} else {
				addr = randPrefix(rng).Addr()
			}

			rec, matched, err := store.LookupIP(addr)
			if err != nil {
				store.Close()
				t.Fatalf("round %d LookupIP(%s) error = %v", round, addr, err)
			}

			wantPrefix, wantMatched := bruteForceLookupIP(prefixes, addr)
			if matched != wantMatched {
				store.Close()
				t.Fatalf("round %d LookupIP(%s) matched=%v, want %v\nprefixes=%v",
					round, addr, matched, wantMatched, prefixes)
			}
			if matched && rec.Network != wantPrefix.String() {
				store.Close()
				t.Fatalf("round %d LookupIP(%s) Network=%q, want %q\nprefixes=%v",
					round, addr, rec.Network, wantPrefix.String(), prefixes)
			}
		}
		store.Close()
	}
}

// TestLookupPropertyCIDR property test：随机 prefix 集下 LookupCIDR 与暴力 oracle 一致。
func TestLookupPropertyCIDR(t *testing.T) {
	rng := rand.New(rand.NewSource(137))

	const rounds = 200
	for round := 0; round < rounds; round++ {
		n := 5 + rng.Intn(11)
		raw := make([]netip.Prefix, n)
		for i := range raw {
			raw[i] = randPrefix(rng)
		}
		prefixes := dedupPrefixes(raw)

		rootDir := buildPrefixFixture(t, "prop-cidr", prefixes)
		store := openV2Fixture(t, rootDir, "prop-cidr")

		for q := 0; q < 8; q++ {
			query := randPrefix(rng)

			records, err := store.LookupCIDR(query)
			if err != nil {
				store.Close()
				t.Fatalf("round %d LookupCIDR(%s) error = %v", round, query, err)
			}

			want := bruteForceLookupCIDR(prefixes, query)
			gotNetworks := make([]string, len(records))
			for i, r := range records {
				gotNetworks[i] = r.Network
			}
			wantNetworks := make([]string, len(want))
			for i, p := range want {
				wantNetworks[i] = p.String()
			}
			if !equalStrings(gotNetworks, wantNetworks) {
				store.Close()
				t.Fatalf("round %d LookupCIDR(%s)\n got=%v\nwant=%v\nprefixes=%v",
					round, query, gotNetworks, wantNetworks, prefixes)
			}
		}
		store.Close()
	}
}

// BenchmarkLookupIPV4Cold 基准：IPv4 冷缓存单 IP 查询（库含 ~1000 个 V4 网段）。
// ladder 最多 33 次 point Get，验证性能可接受（roadmap §7）。
func BenchmarkLookupIPV4Cold(b *testing.B) {
	store, addr := setupLookupBenchmark(b, false)
	defer store.Close()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, _ = store.LookupIP(addr)
	}
}

// BenchmarkLookupIPV6Cold 基准：IPv6 冷缓存单 IP 查询（库含 ~1000 个 V6 网段）。
// ladder 最多 129 次 point Get。
func BenchmarkLookupIPV6Cold(b *testing.B) {
	store, addr := setupLookupBenchmark(b, true)
	defer store.Close()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, _ = store.LookupIP(addr)
	}
}

// setupLookupBenchmark 构建一个含 ~1000 个网段的 v2 库，返回 store 与一个命中地址。
func setupLookupBenchmark(b *testing.B, v6 bool) (*BaseStore, netip.Addr) {
	b.Helper()
	rng := rand.New(rand.NewSource(7))
	var prefixes []netip.Prefix
	for i := 0; i < 1000; i++ {
		if v6 {
			var raw [16]byte
			rng.Read(raw[:])
			// 固定 /48 便于排序 + 保证有命中。
			prefixes = append(prefixes, netip.PrefixFrom(netip.AddrFrom16(raw), 48).Masked())
		} else {
			var raw [4]byte
			rng.Read(raw[:])
			prefixes = append(prefixes, netip.PrefixFrom(netip.AddrFrom4(raw), 24).Masked())
		}
	}
	// 排序去重后构建。
	prefixes = dedupPrefixes(prefixes)
	sort.Slice(prefixes, func(i, j int) bool {
		fi, ai := familyAndAddrBytes(prefixes[i])
		fj, aj := familyAndAddrBytes(prefixes[j])
		if fi != fj {
			return fi < fj
		}
		return bytes.Compare(ai, aj) < 0
	})

	var lines []string
	lines = append(lines, strings.Join(expectedCSVHeader, ","))
	for _, p := range prefixes {
		lines = append(lines, p.String()+",X,X,X,X,AS,AS,as.example")
	}
	rootDir := b.TempDir()
	csvPath := filepath.Join(b.TempDir(), "bench.csv")
	if err := os.WriteFile(csvPath, []byte(strings.Join(lines, "\n")), 0644); err != nil {
		b.Fatalf("write CSV error = %v", err)
	}
	if _, err := buildV2FromCSV(rootDir, BuildOptions{CSVPath: csvPath, BuildID: "bench"}); err != nil {
		b.Fatalf("buildV2FromCSV error = %v", err)
	}
	store, err := openBaseV2(rootDir, "bench")
	if err != nil {
		b.Fatalf("openBaseV2 error = %v", err)
	}
	// 取库内首个 prefix 的地址作为命中查询（保证 ladder 命中而非全 MISS）。
	addr := prefixes[0].Addr()
	return store, addr
}

func dedupPrefixes(in []netip.Prefix) []netip.Prefix {
	seen := map[string]bool{}
	var out []netip.Prefix
	for _, p := range in {
		s := p.String()
		if !seen[s] {
			seen[s] = true
			out = append(out, p)
		}
	}
	return out
}

func randomAddrInPrefix(rng *rand.Rand, p netip.Prefix) netip.Addr {
	// 在 prefix 范围内取随机地址：保留网络位，主机位随机填充。
	addr := p.Addr()
	maxBits := addr.BitLen()
	hostBits := maxBits - p.Bits()
	if hostBits == 0 {
		return addr
	}
	// 把 addr 转成字节，主机位部分用随机字节覆盖。
	var raw []byte
	if addr.Is4() {
		a4 := addr.As4()
		raw = a4[:]
	} else {
		a16 := addr.As16()
		raw = a16[:]
	}
	// 从网络位末尾开始随机化主机位（简化：对主机字节全随机）。
	startByte := (p.Bits() + 7) / 8
	for i := startByte; i < len(raw); i++ {
		raw[i] = byte(rng.Intn(256))
	}
	// 处理网络位最后字节的剩余 host bits（用随机值填充，可能超出 prefix 但 Contains 仍成立）。
	if addr.Is4() {
		return netip.AddrFrom4([4]byte{raw[0], raw[1], raw[2], raw[3]})
	}
	var a16 [16]byte
	copy(a16[:], raw)
	return netip.AddrFrom16(a16)
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
