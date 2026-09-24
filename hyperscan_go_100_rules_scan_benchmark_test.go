package scankit

import (
	"fmt"
	"math/rand/v2"
	"testing"
)

var (
	engineScanSink []Match

	engineScanCorpusSizes = []int{
		1 << 10,  // 1 KiB
		1 << 20,  // 1 MiB
		10 << 20, // 10 MiB
		// 100 << 20, // 100 MiB
		// 500 << 20, // 500 MiB
	}

	// 每 MiB 插入多少个正向样本。
	//
	// 注意：
	// 这里控制的是 Samples 数量，而不是最终 Match 数量。
	// 如果多个 Rule 可以同时命中一个 Sample，
	// 最终 matches/op 可能明显大于这里的值。
	engineScanSampleDensities = []int{
		0,
		10,
		100,
		1000,
		10000,
	}
)

func BenchmarkEngineScan(b *testing.B) {
	expressions := make([]Expression, 0, len(Rules))

	for index, pattern := range Rules {
		expressions = append(expressions, Expression{
			Id:      uint32(index + 1),
			Pattern: pattern,
		})
	}

	scanner, err := Compile(expressions)
	if err != nil {
		b.Fatal(err)
	}

	for _, size := range engineScanCorpusSizes {
		for _, samplesPerMB := range engineScanSampleDensities {
			name := fmt.Sprintf(
				"%s_%dsampleMB",
				formatBenchmarkSize(size),
				samplesPerMB,
			)

			b.Run(name, func(b *testing.B) {
				data := buildBenchmarkCorpus(
					size,
					samplesPerMB,
				)

				// 先做一次实际扫描。
				//
				// 目的：
				// 1. 验证 corpus 没有问题
				// 2. 得到真实 match 数量
				// 3. 根据真实 match 数量预估 capacity
				matches, err := scanner.scanInto(data, nil)
				if err != nil {
					b.Fatal(err)
				}

				actualMatches := len(matches)

				b.ReportMetric(
					float64(actualMatches),
					"initial-matches/op",
				)

				// 根据第一次扫描结果预分配。
				//
				// 这样可以避免 benchmark 因为 []Match 扩容，
				// 把“扫描性能”和“slice 扩容性能”混在一起。
				matchCapacity := max(actualMatches, 16)

				b.SetBytes(int64(len(data)))
				b.ReportAllocs()

				b.ResetTimer()

				var totalMatches int64

				matches = make([]Match, 0, matchCapacity)

				for range b.N {
					matches = matches[:0]

					matches, err = scanner.scanInto(data, matches)
					if err != nil {
						b.Fatal(err)
					}

					totalMatches += int64(len(matches))
				}

				b.StopTimer()

				b.ReportMetric(
					float64(totalMatches)/float64(b.N),
					"matches/op",
				)

				engineScanSink = matches
			})
		}
	}
}

func formatBenchmarkSize(size int) string {
	switch {
	case size >= 1<<30:
		return fmt.Sprintf("%dGB", size>>30)

	case size >= 1<<20:
		return fmt.Sprintf("%dMB", size>>20)

	case size >= 1<<10:
		return fmt.Sprintf("%dKB", size>>10)

	default:
		return fmt.Sprintf("%dB", size)
	}
}

// buildBenchmarkCorpus 构造 benchmark 数据。
//
// samplesPerMB 表示：
// 每 1 MiB 数据插入多少个 Samples。
//
// 注意：
// 这里控制的是“正向样本数量”，不是最终 Hyperscan Match 数量。
func buildBenchmarkCorpus(size int, samplesPerMB int) []byte {
	if size <= 0 {
		return nil
	}

	data := buildNoise(size)

	if samplesPerMB <= 0 || len(Samples) == 0 {
		return data
	}

	// 根据数据大小计算需要插入多少个 Sample。
	sampleCount := size * samplesPerMB / (1 << 20)

	// 小于 1MiB 时，如果 density > 0，
	// 至少插入一个 sample。
	if sampleCount == 0 {
		sampleCount = 1
	}

	// 使用固定 seed，保证 benchmark 每次运行数据一致。
	rng := rand.New(rand.NewPCG(
		uint64(size),
		uint64(samplesPerMB),
	))

	// 把 corpus 划分成 sampleCount 个 segment。
	//
	// 每个 sample 放到自己的 segment 中，
	// 避免不同 Sample 相互覆盖。
	segmentSize := size / sampleCount

	if segmentSize <= 0 {
		return data
	}

	for i := 0; i < sampleCount; i++ {
		sample := Samples[rng.IntN(len(Samples))]

		if len(sample) >= segmentSize {
			continue
		}

		segmentStart := i * segmentSize
		segmentEnd := segmentStart + segmentSize

		// 给 sample 留一点边界空间。
		maxOffset := segmentEnd - len(sample)

		if maxOffset <= segmentStart {
			continue
		}

		offset := segmentStart

		if maxOffset > segmentStart {
			offset += rng.IntN(maxOffset - segmentStart)
		}

		copy(data[offset:], sample)
	}

	return data
}

// buildNoise 生成不会刻意制造大量 regex match 的普通日志文本。
//
// 不再使用：
//
//	abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789
//
// 因为这类随机内容会严重污染：
//
//	\d+
//	[A-Za-z]+
//	[A-Za-z0-9]+
//	\w+
//
// 等规则的 benchmark。
func buildNoise(size int) []byte {
	const alphabet = " "
	const line = "INFO request completed successfully message=hello world\n"

	data := make([]byte, 0, size)

	for len(data)+len(line) <= size {
		data = append(data, line...)
	}

	if len(data) < size {
		remaining := size - len(data)

		for range remaining {
			data = append(data, alphabet[0])
		}
	}

	return data[:size]
}

// TestEngineScanStable 用 testing.AllocsPerRun(200, ...) + testing.Benchmark
// 提供稳定的 ns/op、MB/s、B/op、allocs/op 基线，避免 -benchtime=1s -benchmem
// 在低 alloc 场景下的 cold-start 摊销噪声。
func TestEngineScanStable(t *testing.T) {
	expressions := make([]Expression, 0, len(Rules))
	for index, pattern := range Rules {
		expressions = append(expressions, Expression{
			Id:      uint32(index + 1),
			Pattern: pattern,
		})
	}
	scanner, err := Compile(expressions)
	if err != nil {
		t.Fatal(err)
	}

	type tc struct {
		size    int
		density int
	}
	cases := []tc{
		{1 << 10, 0}, {1 << 10, 10}, {1 << 10, 100}, {1 << 10, 1000}, {1 << 10, 10000},
		{1 << 20, 0}, {1 << 20, 10}, {1 << 20, 100}, {1 << 20, 1000}, {1 << 20, 10000},
		{10 << 20, 0}, {10 << 20, 10}, {10 << 20, 100}, {10 << 20, 1000}, {10 << 20, 10000},
	}

	matches := make([]Match, 0, 16)
	for _, c := range cases {
		data := buildBenchmarkCorpus(c.size, c.density)
		// warm up pool: 10 scans bring ctx to steady state
		for range 10 {
			matches = matches[:0]
			matches, _ = scanner.scanInto(data, matches)
		}
		// AllocsPerRun 内部跑多次取平均，更稳定
		allocs := testing.AllocsPerRun(200, func() {
			matches = matches[:0]
			matches, _ = scanner.scanInto(data, matches)
		})
		// testing.Benchmark 内部 b.N 较大，cold-start 摊销到接近 0
		res := testing.Benchmark(func(b *testing.B) {
			matches := make([]Match, 0, 16)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				matches = matches[:0]
				matches, _ = scanner.scanInto(data, matches)
			}
		})
		nsPerOp := float64(res.NsPerOp())
		mbPerS := float64(c.size) / nsPerOp * 1e9 / (1 << 20)
		t.Logf("size=%dMB density=%d/MB ns/op=%.0f MB/s=%.2f B/op=%d allocs/op=%.1f",
			c.size>>20, c.density, nsPerOp, mbPerS, res.AllocedBytesPerOp(), allocs)
	}
}
