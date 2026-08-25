package scankit_test

import (
	"errors"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/smartwalle/scankit"
)

// commonRegexAtoms 是 byte 与 UTF8|UCP 执行器都具有相同语义的受限子集。所有原子
// 都匹配非空、固定字节宽度的 ASCII 文本；这使扫描器按结束位置交付的结果可以与 Go
// regexp 的重叠起始位置搜索一一比较。Unicode 的点、否定类、简写类和 Unicode POSIX
// 类不在此集合中，因为它们的 byte/UCP 语义并不相同。
var commonRegexAtoms = [...]string{
	"A",
	"b",
	"0",
	"_",
	"ID",
	`\x{41}`,
	`[A-C]`,
	`[0-2]`,
	`[ab]`,
	`[x-z]`,
	`(?:A|b)`,
	`(?:ID|OK)`,
	`[A-C]{2}`,
	`[0-2]{2}`,
}

// generatedCommonRegexPattern 使用确定性的有界状态机生成共同子集表达式。它只在测试中
// 使用，绝不进入 Compile 或扫描热路径。
func generatedCommonRegexPattern(seed uint64) string {
	state := seed
	partCount := int(nextCommonRegexState(&state)%3) + 1
	var pattern strings.Builder
	for range partCount {
		atom := commonRegexAtoms[nextCommonRegexState(&state)%uint64(len(commonRegexAtoms))]
		pattern.WriteString(atom)
	}
	return pattern.String()
}

func nextCommonRegexState(state *uint64) uint64 {
	*state = *state*6364136223846793005 + 1442695040888963407
	return *state
}

func TestGeneratedCommonRegexPatternsCompileForByteUCPAndGoRegexp(t *testing.T) {
	t.Parallel()
	seen := make(map[string]struct{})
	for seed := uint64(0); seed < 1_024; seed++ {
		pattern := generatedCommonRegexPattern(seed)
		seen[pattern] = struct{}{}
		if len(pattern) == 0 || len(pattern) > 30 {
			t.Fatalf("pattern generated from seed %d has invalid byte length %d: %q", seed, len(pattern), pattern)
		}
		if _, err := regexp.Compile(pattern); err != nil {
			t.Fatalf("Go regexp Compile(%q): %v", pattern, err)
		}
		for _, flags := range []scankit.CompileFlag{0, scankit.CompileUTF8 | scankit.CompileUnicodeProperties} {
			if _, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: pattern, Flags: flags}}); err != nil {
				t.Fatalf("scankit Compile(%q, flags=%#x): %v", pattern, flags, err)
			}
		}
	}
	if len(seen) < 256 {
		t.Fatalf("generated only %d distinct patterns, want at least 256", len(seen))
	}
}

func TestGeneratedCommonRegexPatternsPreserveRangesAndEndOffsets(t *testing.T) {
	t.Parallel()
	data := []byte("A1 ID=AB 状态=XY token=ab0\nOK=AABC_012")
	for _, seed := range []uint64{0, 1, 2, 3, 17, 42, 255, 1_024} {
		t.Run("seed_"+strconv.FormatUint(seed, 10), func(t *testing.T) {
			assertGeneratedCommonRegexMatchesGoRegexp(t, seed, data)
		})
	}
}

func FuzzGeneratedCommonRegexByteAndUCPMatchGoRegexp(f *testing.F) {
	for _, seed := range []uint64{0, 1, 2, 3, 17, 42, 255, 1_024, ^uint64(0)} {
		f.Add(seed, []byte("A1 ID=AB 状态=XY token=ab0\nOK=AABC_012"))
	}
	f.Add(uint64(7), []byte{})
	f.Add(uint64(42), []byte{'A', 0xff, 'B'})

	f.Fuzz(func(t *testing.T, seed uint64, data []byte) {
		if len(data) > 4_096 {
			t.Skip()
		}
		pattern := generatedCommonRegexPattern(seed)
		goRegexp, err := regexp.Compile(pattern)
		if err != nil {
			t.Fatalf("Go regexp Compile(%q): %v", pattern, err)
		}
		byteScanner, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: pattern}})
		if err != nil {
			t.Fatalf("byte Compile(%q): %v", pattern, err)
		}
		byteRanges := scanGeneratedCommonRegexRanges(t, byteScanner, data)
		assertRangesEqual(t, byteRanges, findAllOverlapping(goRegexp, data))

		ucpScanner, err := scankit.Compile([]scankit.Expression{{
			Id:      1,
			Pattern: pattern,
			Flags:   scankit.CompileUTF8 | scankit.CompileUnicodeProperties,
		}})
		if err != nil {
			t.Fatalf("UCP Compile(%q): %v", pattern, err)
		}
		if !utf8.Valid(data) {
			if _, err := ucpScanner.Scan(data); !errors.Is(err, scankit.ErrInvalidUTF8) {
				t.Fatalf("UCP Scan(%q) error = %v, want ErrInvalidUTF8", data, err)
			}
			if _, err := ucpScanner.ScanInto(data, nil); !errors.Is(err, scankit.ErrInvalidUTF8) {
				t.Fatalf("UCP ScanInto(%q) error = %v, want ErrInvalidUTF8", data, err)
			}
			return
		}
		ucpRanges := scanGeneratedCommonRegexRanges(t, ucpScanner, data)
		assertRangesEqual(t, ucpRanges, findAllOverlapping(goRegexp, data))
	})
}

func assertGeneratedCommonRegexMatchesGoRegexp(t *testing.T, seed uint64, data []byte) {
	t.Helper()
	pattern := generatedCommonRegexPattern(seed)
	goRegexp := regexp.MustCompile(pattern)
	want := findAllOverlapping(goRegexp, data)
	for _, flags := range []scankit.CompileFlag{0, scankit.CompileUTF8 | scankit.CompileUnicodeProperties} {
		scanner, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: pattern, Flags: flags}})
		if err != nil {
			t.Fatalf("Compile(%q, flags=%#x): %v", pattern, flags, err)
		}
		assertRangesEqual(t, scanGeneratedCommonRegexRanges(t, scanner, data), want)
	}
}

func scanGeneratedCommonRegexRanges(t testing.TB, scanner *scankit.Scanner, data []byte) [][2]int {
	t.Helper()
	matches, err := scanner.Scan(data)
	if err != nil {
		t.Fatal(err)
	}
	into, err := scanner.ScanInto(data, make([]scankit.Match, 0, len(matches)))
	if err != nil {
		t.Fatal(err)
	}
	if len(into) != len(matches) {
		t.Fatalf("ScanInto match count = %d, want %d", len(into), len(matches))
	}
	for index := range matches {
		if into[index] != matches[index] {
			t.Fatalf("ScanInto match %d = %#v, want %#v", index, into[index], matches[index])
		}
	}
	ranges := make([][2]int, len(matches))
	for index, match := range matches {
		if match.Id != 1 || match.From >= match.To || match.To > uint64(len(data)) {
			t.Fatalf("invalid generated-pattern match %#v for %d-byte input", match, len(data))
		}
		ranges[index] = [2]int{int(match.From), int(match.To)}
	}
	return ranges
}
