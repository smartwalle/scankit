// rose_bench_test：Rose 直通路径（scanRoseDirectInto）在 Scanner 层的基准。
//
// Rose 直通路径的唯一生产场景是「全部规则都是单条纯文字 + 至少一条带
// SingleMatch/Quiet」：全部规则都是纯文字且不带这两个标志时，scanner.go 的
// fastLiteral 判定会抢先；带锚定/末尾约束或需要二次确认时，
// computeCanUseRoseInScan 又会返回 false。此前 tests 包的端到端基准没有任何
// 一个构造该形态，因此 Rose 直通路径在 Scanner 层完全没有基准覆盖。
//
// 本基准用同一组 100 条纯文字，只切换最后一条的 SingleMatch，分别驱动
// Rose 直通路径与 fastLiteral 对照路径；两者在 0 命中语料上结果一致，
// 在有命中语料上也应一致（这些规则不会在同一位置重复匹配）。

package tests

import (
	"fmt"
	"testing"

	"github.com/smartwalle/scankit"
)

const roseBenchSentence = "The quick brown fox jumps over the lazy dog while packing boxes. "

var roseBenchMatchesSink []scankit.Match

// roseBenchExpressions 构造 99 条 kw_lit_NNN 纯文字加 1 条 kw_single。
// single 为真时最后一条带 CompileSingleMatch，使 fastLiteral 让位给 Rose。
func roseBenchExpressions(single bool) []scankit.Expression {
	expressions := make([]scankit.Expression, 0, 100)
	for i := 0; i < 99; i++ {
		expressions = append(expressions, scankit.Expression{
			Id:      uint32(i + 1),
			Pattern: fmt.Sprintf("kw_lit_%03d", i),
		})
	}
	flags := scankit.CompileFlag(0)
	if single {
		flags = scankit.CompileSingleMatch
	}
	return append(expressions, scankit.Expression{Id: 100, Pattern: "kw_single", Flags: flags})
}

// roseBenchEnglishData 生成英文日志型语料：100 条规则共享首字节 k，
// 该语料让 k 平均每 32.5 B 出现一次，是首字节命中密集的形态。
func roseBenchEnglishData(n int) []byte {
	data := make([]byte, 0, n+len(roseBenchSentence))
	for len(data) < n {
		data = append(data, roseBenchSentence...)
	}
	return data[:n]
}

// roseBenchSparseData 生成不命中任何规则首字节的稀疏语料。
func roseBenchSparseData(n int) []byte {
	data := make([]byte, n)
	for i := range data {
		data[i] = '.'
	}
	return data
}

func roseBenchScan(b *testing.B, data []byte, single bool) {
	scanner, err := scankit.Compile(roseBenchExpressions(single))
	if err != nil {
		b.Fatal(err)
	}
	matches, err := scanner.ScanInto(data, nil)
	if err != nil {
		b.Fatal(err)
	}
	want := len(matches)
	b.ReportAllocs()
	b.SetBytes(int64(len(data)))
	b.ResetTimer()
	for b.Loop() {
		matches = matches[:0]
		matches, err = scanner.ScanInto(data, matches)
		if err != nil {
			b.Fatal(err)
		}
		if len(matches) != want {
			b.Fatalf("命中数变化: got=%d want=%d", len(matches), want)
		}
	}
	roseBenchMatchesSink = matches
}

// BenchmarkRoseDirectScan 在同一语料上对比 Rose 直通路径与 fastLiteral 对照路径。
func BenchmarkRoseDirectScan(b *testing.B) {
	const n = 64 << 10
	corpora := []struct {
		name string
		data []byte
	}{
		{"english", roseBenchEnglishData(n)},
		{"sparse", roseBenchSparseData(n)},
	}
	for _, corpus := range corpora {
		b.Run(corpus.name+"/rose", func(b *testing.B) { roseBenchScan(b, corpus.data, true) })
		b.Run(corpus.name+"/unified", func(b *testing.B) { roseBenchScan(b, corpus.data, false) })
	}
}

// BenchmarkRoseDirectScanSizes 按输入长度扫描 Rose 直通路径与 fastLiteral 对照路径，
// 用于定位两条路径在 Scanner 层的交叉点。
func BenchmarkRoseDirectScanSizes(b *testing.B) {
	for _, n := range []int{8, 16, 64, 256, 1 << 10, 4 << 10, 16 << 10, 64 << 10} {
		data := roseBenchEnglishData(n)
		b.Run(fmt.Sprintf("%dB/rose", n), func(b *testing.B) { roseBenchScan(b, data, true) })
		b.Run(fmt.Sprintf("%dB/unified", n), func(b *testing.B) { roseBenchScan(b, data, false) })
	}
}

// TestRoseDirectScanMatchesFastLiteralPath 断言两条路径在稀疏、英文型与含命中
// 三类语料上给出相同结果，保证基准对照有意义。
func TestRoseDirectScanMatchesFastLiteralPath(t *testing.T) {
	corpora := map[string][]byte{
		"sparse":  roseBenchSparseData(64 << 10),
		"english": roseBenchEnglishData(64 << 10),
		"hit":     []byte("xx kw_single yy kw_lit_000 zz"),
	}
	for name, data := range corpora {
		roseScanner, err := scankit.Compile(roseBenchExpressions(true))
		if err != nil {
			t.Fatal(err)
		}
		unifiedScanner, err := scankit.Compile(roseBenchExpressions(false))
		if err != nil {
			t.Fatal(err)
		}
		roseMatches, err := roseScanner.ScanInto(data, nil)
		if err != nil {
			t.Fatal(err)
		}
		unifiedMatches, err := unifiedScanner.ScanInto(data, nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(roseMatches) != len(unifiedMatches) {
			t.Fatalf("语料 %s 命中数不一致: rose=%d unified=%d", name, len(roseMatches), len(unifiedMatches))
		}
		for i := range roseMatches {
			if roseMatches[i] != unifiedMatches[i] {
				t.Fatalf("语料 %s 第 %d 个命中不一致: rose=%+v unified=%+v", name, i, roseMatches[i], unifiedMatches[i])
			}
		}
	}
}
