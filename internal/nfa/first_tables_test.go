package nfa

import (
	"fmt"
	"math/rand"
	"testing"

	"github.com/smartwalle/scankit/internal/dispatch"
	"github.com/smartwalle/scankit/internal/nfagraph"
	"github.com/smartwalle/scankit/internal/parser"
	"github.com/smartwalle/scankit/internal/simd"
)

// nfaStartPositions 收集 forEachNFAStart 枚举出的起点，便于与参考实现逐点比对。
func nfaStartPositions(data []byte, first [4]uint64, tables *simd.ByteSetTables, acceptsEmpty bool) []int {
	var out []int
	forEachNFAStart(data, nil, first, tables, acceptsEmpty, dispatch.DefaultBackend(), func(start int) bool {
		out = append(out, start)
		return true
	})
	return out
}

// expectedNFAStarts 用逐字节扫描给出 forEachNFAStart 的参考结果，覆盖可接受空匹配
// 与空集合两种入口分支的边界语义。
func expectedNFAStarts(data []byte, first [4]uint64, acceptsEmpty bool) []int {
	if acceptsEmpty {
		out := make([]int, 0, len(data)+1)
		for start := 0; start <= len(data); start++ {
			out = append(out, start)
		}
		return out
	}
	if byteMaskEmpty(first) {
		out := make([]int, 0, len(data))
		for start := range data {
			out = append(out, start)
		}
		return out
	}
	out := make([]int, 0, len(data))
	for start := range data {
		if first[data[start]/64]&(1<<uint(data[start]%64)) != 0 {
			out = append(out, start)
		}
	}
	return out
}

// TestForEachNFAStartWindowMatchesRawBitmap 校验预编译窗口路径与原始位图回退路径
// 在全部长度（含 32 字节窗口边界与尾部截断）上给出完全一致的起点序列。
func TestForEachNFAStartWindowMatchesRawBitmap(t *testing.T) {
	rng := rand.New(rand.NewSource(20260920))
	sets := []struct {
		name  string
		first [4]uint64
	}{
		{name: "single", first: maskOfBytes('a')},
		{name: "sparse", first: maskOfBytes('a', 'f', '0', 'y', 0xff)},
		{name: "dense", first: maskOfBytes(byteRange('a', 'z')...)},
		{name: "empty", first: [4]uint64{}},
	}
	for _, set := range sets {
		tables := simd.FirstByteTables(set.first)
		for length := 0; length <= 80; length++ {
			// 小字母表制造密集命中，下标 0/31/32 等窗口边界必须与标量语义一致。
			data := make([]byte, length)
			for i := range data {
				data[i] = byte('a' + rng.Intn(6))
			}
			for _, acceptsEmpty := range []bool{false, true} {
				want := expectedNFAStarts(data, set.first, acceptsEmpty)
				got := nfaStartPositions(data, set.first, &tables, acceptsEmpty)
				if fmt.Sprint(got) != fmt.Sprint(want) {
					t.Fatalf("集合=%s 长度=%d acceptsEmpty=%v got=%v want=%v", set.name, length, acceptsEmpty, got, want)
				}
				legacy := nfaStartPositions(data, set.first, nil, acceptsEmpty)
				if fmt.Sprint(got) != fmt.Sprint(legacy) {
					t.Fatalf("集合=%s 长度=%d acceptsEmpty=%v 窗口路径=%v 原始位图路径=%v", set.name, length, acceptsEmpty, got, legacy)
				}
			}
		}
	}
}

// TestForEachNFAStartWindowCoversLongInput 固定长输入上的候选位置，确认窗口步进
// 与尾部逐字节补齐既不漏报也不重复。
func TestForEachNFAStartWindowCoversLongInput(t *testing.T) {
	data := make([]byte, 100)
	data[0], data[31], data[32], data[63], data[64], data[99] = 'a', 'a', 'a', 'a', 'a', 'a'
	first := maskOfBytes('a')
	tables := simd.FirstByteTables(first)
	got := nfaStartPositions(data, first, &tables, false)
	want := []int{0, 31, 32, 63, 64, 99}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("窗口起点=%v want=%v", got, want)
	}
}

// TestEngineFamiliesWindowBoundaryConformance 校验专用布局的批量起点枚举在 32 字节
// 窗口边界附近不丢候选：同一规则在多种长度与随机内容的数据上与通用 NFA 参考实现
// 逐条比对结果集合。
func TestEngineFamiliesWindowBoundaryConformance(t *testing.T) {
	patterns := []string{
		`foobar`,
		`(?:foo|bar)`,
		`[a-c]xy?`,
		`a[0-9]{2}`,
		`(?:ab|a[0-9])+`,
	}
	lengths := []int{0, 1, 15, 16, 31, 32, 33, 47, 63, 64, 65, 70}
	kinds := []EngineKind{
		EngineCastle, EngineGough, EngineLimEx, EngineSheng, EngineMcSheng,
		EngineTamarama, EngineVermicelli, EngineShufti, EngineTruffle,
		EngineRepeat, EngineMPV, EngineLBR,
	}
	rng := rand.New(rand.NewSource(4207))
	alphabet := []byte("abfo0r1c2xy9")
	for _, pattern := range patterns {
		root, err := parser.Parse(pattern)
		if err != nil {
			t.Fatalf("模式=%q 解析失败: %v", pattern, err)
		}
		g, err := nfagraph.NewBuilder().Build(root)
		if err != nil {
			t.Fatalf("模式=%q 建图失败: %v", pattern, err)
		}
		ref := &Program{Graph: g}
		engines := make([]*Engine, 0, len(kinds))
		for _, kind := range kinds {
			e, err := CompileEngine(g, kind)
			if err != nil {
				t.Fatalf("模式=%q 引擎=%s 编译失败: %v", pattern, kind, err)
			}
			engines = append(engines, e)
		}
		for _, length := range lengths {
			data := make([]byte, length)
			for i := range data {
				data[i] = alphabet[rng.Intn(len(alphabet))]
			}
			want := fmt.Sprint(ref.Spans(data))
			for i, e := range engines {
				if got := fmt.Sprint(e.Spans(data)); got != want {
					t.Fatalf("模式=%q 引擎=%s 长度=%d 数据=%q got=%v want=%v", pattern, kinds[i], length, data, got, want)
				}
			}
		}
	}
}

// BenchmarkForEachNFAStartFirstByte 对照预编译窗口路径与原始位图回退路径的起点枚举成本。
func BenchmarkForEachNFAStartFirstByte(b *testing.B) {
	data := make([]byte, 64<<10)
	for i := range data {
		data[i] = byte('a' + i%7)
	}
	first := maskOfBytes('a', 'c', 'e')
	tables := simd.FirstByteTables(first)
	cases := []struct {
		name   string
		tables *simd.ByteSetTables
	}{
		{name: "window32", tables: &tables},
		{name: "raw16", tables: nil},
	}
	for _, tc := range cases {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				count := 0
				forEachNFAStart(data, nil, first, tc.tables, false, dispatch.DefaultBackend(), func(int) bool {
					count++
					return true
				})
				if count == 0 {
					b.Fatal("没有候选起点")
				}
			}
		})
	}
}

// maskOfBytes 构造包含给定字节的 256 位集合。
func maskOfBytes(values ...byte) [4]uint64 {
	var mask [4]uint64
	for _, v := range values {
		mask[v/64] |= 1 << uint(v%64)
	}
	return mask
}

// byteRange 返回闭区间 [lo, hi] 上的全部字节值。
func byteRange(lo, hi byte) []byte {
	out := make([]byte, 0, int(hi)-int(lo)+1)
	for v := int(lo); v <= int(hi); v++ {
		out = append(out, byte(v))
	}
	return out
}
