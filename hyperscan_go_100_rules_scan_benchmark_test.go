package scankit

import (
	"fmt"
	"math/rand/v2"
	"testing"
)

var (
	engineScanSink any

	engineScanCorpusSizes = []int{
		1 << 10, // 1 KiB
		1 << 20, // 1 MiB
		//10 << 20, // 10 MiB
		//100 << 20, // 100 MiB
		//500 << 20, // 500 MiB
	}
)

func BenchmarkEngineScan(b *testing.B) {
	expressions := make([]Expression, 0, len(Rules))
	for index, pattern := range Rules {
		expressions = append(expressions, Expression{Id: uint32(index + 1), Pattern: pattern})
	}

	scanner, err := Compile(expressions)
	if err != nil {
		b.Fatal(err)
	}

	for _, size := range engineScanCorpusSizes {
		b.Run(formatBenchmarkSize(size), func(b *testing.B) {
			data := buildBenchmarkCorpus(size)

			b.SetBytes(int64(len(data)))
			b.ReportAllocs()

			b.ResetTimer()

			var matches = make([]Match, 0, 1000)

			for range b.N {
				matches = matches[:0]
				matches, err = scanner.scanInto(data, matches)
				if err != nil {
					b.Fatal(err)
				}

				engineScanSink = matches
			}
		})
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

func buildBenchmarkCorpus(size int) []byte {
	const (
		noiseRatio = 0.90
		matchRatio = 0.10
	)

	data := make([]byte, 0, size)

	noiseSize := int(float64(size) * noiseRatio)
	matchSize := size - noiseSize

	// 90% 普通文本
	data = append(data, buildNoise(noiseSize)...)

	// 10% 正向样本
	data = append(data, buildPositive(matchSize)...)

	return data[:size]
}

func buildNoise(size int) []byte {
	const alphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789 ,.;:!?-_ "

	data := make([]byte, size)

	for i := range data {
		data[i] = alphabet[rand.IntN(len(alphabet))]
	}

	return data
}

func buildPositive(size int) []byte {
	if size <= 0 {
		return nil
	}

	data := make([]byte, 0, size)

	for len(data) < size {
		sample := Samples[rand.IntN(len(Samples))]

		if len(data)+len(sample)+1 > size {
			break
		}

		data = append(data, sample...)
		data = append(data, ' ')
	}

	// 如果最后不足，用普通噪声补齐。
	if len(data) < size {
		data = append(data, buildNoise(size-len(data))...)
	}

	return data[:size]
}
