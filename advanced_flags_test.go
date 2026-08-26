package scankit_test

import (
	"errors"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/smartwalle/scankit"
)

func TestAllowEmptyReportsZeroWidthMatchesAtEveryBoundary(t *testing.T) {
	t.Parallel()
	db, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: `a*`, Flags: scankit.CompileAllowEmpty}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := db.Scan([]byte("aa"))
	if err != nil {
		t.Fatal(err)
	}
	assertMatchesEqual(t, got, []scankit.Match{
		{Id: 1, From: 0, To: 0},
		{Id: 1, From: 0, To: 1},
		{Id: 1, From: 1, To: 1},
		{Id: 1, From: 0, To: 2},
		{Id: 1, From: 1, To: 2},
		{Id: 1, From: 2, To: 2},
	})
}

func TestSOMLeftmostSuppressesLaterStartsAtSameEnd(t *testing.T) {
	t.Parallel()
	db, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: `a+`, Flags: scankit.CompileLeftmostStart}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := db.Scan([]byte("aaa"))
	if err != nil {
		t.Fatal(err)
	}
	assertMatchesEqual(t, got, []scankit.Match{{Id: 1, From: 0, To: 1}, {Id: 1, From: 0, To: 2}, {Id: 1, From: 0, To: 3}})
}

func TestPrefilterUsesExactExecutorForSupportedSyntax(t *testing.T) {
	t.Parallel()
	exact, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: `\d+`}})
	if err != nil {
		t.Fatal(err)
	}
	prefilter, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: `\d+`, Flags: scankit.CompilePrefilter}})
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("a12b3")
	want, err := exact.Scan(data)
	if err != nil {
		t.Fatal(err)
	}
	got, err := prefilter.Scan(data)
	if err != nil {
		t.Fatal(err)
	}
	assertMatchesEqual(t, got, want)
}

func TestHammingDistanceMatchesFixedWidthLiterals(t *testing.T) {
	t.Parallel()
	db, err := scankit.Compile([]scankit.Expression{{
		Id:      1,
		Pattern: "token",
		Ext:     &scankit.ExpressionExt{Flags: scankit.ExtHammingDistance, HammingDistance: 1},
	}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := db.Scan([]byte("token toxen tokan tokxx"))
	if err != nil {
		t.Fatal(err)
	}
	assertMatchesEqual(t, got, []scankit.Match{{Id: 1, From: 0, To: 5}, {Id: 1, From: 6, To: 11}, {Id: 1, From: 12, To: 17}})
}

func TestHammingDistanceMatchesFixedWidthByteRegex(t *testing.T) {
	t.Parallel()
	db, err := scankit.Compile([]scankit.Expression{{
		Id:      1,
		Pattern: `ID:\d{2}`,
		Ext:     &scankit.ExpressionExt{Flags: scankit.ExtHammingDistance, HammingDistance: 1},
	}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := db.Scan([]byte("ID:42 ID:4x IX:4x"))
	if err != nil {
		t.Fatal(err)
	}
	assertMatchesEqual(t, got, []scankit.Match{{Id: 1, From: 0, To: 5}, {Id: 1, From: 6, To: 11}})
}

func TestHammingDistanceMatchesBoundedNFAProduct(t *testing.T) {
	t.Parallel()
	// 此表达式展开后有 512 个固定分支，超过字符类序列执行器有意设置的 64 分支上限，
	// 因此会覆盖有界 NFA×汉明距离乘积执行器。
	db, err := scankit.Compile([]scankit.Expression{{
		Id:      1,
		Pattern: `(?:A|B){9}`,
		Ext:     &scankit.ExpressionExt{Flags: scankit.ExtHammingDistance, HammingDistance: 1},
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		data string
		want []scankit.Match
	}{
		{name: "exact", data: "AAAAAAAAA", want: []scankit.Match{{Id: 1, From: 0, To: 9}}},
		{name: "one_substitution", data: "AAAAAAAAQ", want: []scankit.Match{{Id: 1, From: 0, To: 9}}},
		{name: "two_substitutions", data: "AAAAAAAQQ", want: nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := db.Scan([]byte(test.data))
			if err != nil {
				t.Fatal(err)
			}
			assertMatchesEqual(t, got, test.want)
		})
	}
}

func TestHammingNFAProductRejectsUnboundedResourceOrLanguageShapes(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name     string
		pattern  string
		distance uint32
	}{
		{name: "variable_width", pattern: `(?:A|B){1,9}`, distance: 1},
		{name: "width_limit", pattern: `(?:A|B){257}`, distance: 1},
		{name: "distance_limit", pattern: `(?:A|B){9}`, distance: 65},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := scankit.Compile([]scankit.Expression{{
				Id:      1,
				Pattern: test.pattern,
				Ext:     &scankit.ExpressionExt{Flags: scankit.ExtHammingDistance, HammingDistance: test.distance},
			}})
			if !errors.Is(err, scankit.ErrUnsupportedExtension) {
				t.Fatalf("Compile(%q) error = %v, want ErrUnsupportedExtension", test.pattern, err)
			}
		})
	}
}

func TestHammingDistanceRejectsVariableWidthFixedExecutor(t *testing.T) {
	_, err := scankit.Compile([]scankit.Expression{{
		Id:      1,
		Pattern: `\d{2,3}`,
		Ext:     &scankit.ExpressionExt{Flags: scankit.ExtHammingDistance, HammingDistance: 1},
	}})
	if !errors.Is(err, scankit.ErrUnsupportedExtension) {
		t.Fatalf("Compile() error = %v, want ErrUnsupportedExtension", err)
	}
}

func TestEditDistanceMatchesBoundedVariableWidthByteClassRepeat(t *testing.T) {
	t.Parallel()
	lowered, err := scankit.Compile([]scankit.Expression{{
		Id:      1,
		Pattern: `(?:A|B){6,8}`,
		Ext:     &scankit.ExpressionExt{Flags: scankit.ExtEditDistance, EditDistance: 1},
	}})
	if err != nil {
		t.Fatal(err)
	}
	reference, err := scankit.Compile([]scankit.Expression{{
		Id:      2,
		Pattern: `(?:[AB]{6}|[AB]{7}|[AB]{8})`,
		Ext:     &scankit.ExpressionExt{Flags: scankit.ExtEditDistance, EditDistance: 1},
	}})
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("prefix=ABABABAQ suffix=ABABABAB")
	got, err := lowered.Scan(data)
	if err != nil {
		t.Fatal(err)
	}
	want, err := reference.Scan(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("match count = %d, want %d; got=%#v want=%#v", len(got), len(want), got, want)
	}
	for index := range got {
		if got[index].From != want[index].From || got[index].To != want[index].To {
			t.Fatalf("range[%d] = [%d,%d), want [%d,%d)", index, got[index].From, got[index].To, want[index].From, want[index].To)
		}
	}
}

func TestEditDistanceMatchesBoundedFixedWidthByteClassRepeat(t *testing.T) {
	t.Parallel()
	db, err := scankit.Compile([]scankit.Expression{{
		Id:      1,
		Pattern: `(?:A|B){9}`,
		Ext:     &scankit.ExpressionExt{Flags: scankit.ExtEditDistance, EditDistance: 1},
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		data string
		want []scankit.Match
	}{
		{name: "exact", data: "AAAAAAAAA", want: []scankit.Match{{Id: 1, From: 0, To: 8}, {Id: 1, From: 0, To: 9}}},
		{name: "deletion", data: "AAAAAAAA", want: []scankit.Match{{Id: 1, From: 0, To: 8}}},
		{name: "insertion", data: "AAAAAAAAAQ", want: []scankit.Match{{Id: 1, From: 0, To: 8}, {Id: 1, From: 0, To: 9}, {Id: 1, From: 0, To: 10}}},
		{name: "two_substitutions", data: "AAAAAAAQQ", want: nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := db.Scan([]byte(test.data))
			if err != nil {
				t.Fatal(err)
			}
			assertMatchesEqual(t, got, test.want)
		})
	}
}

func TestEditDistanceBoundedFixedWidthByteClassRepeatMatchesFixedClassReference(t *testing.T) {
	t.Parallel()
	product, err := scankit.Compile([]scankit.Expression{{
		Id:      1,
		Pattern: `(?:A|B){7}`,
		Ext:     &scankit.ExpressionExt{Flags: scankit.ExtEditDistance, EditDistance: 1},
	}})
	if err != nil {
		t.Fatal(err)
	}
	reference, err := scankit.Compile([]scankit.Expression{{
		Id:      2,
		Pattern: `[AB]{7}`,
		Ext:     &scankit.ExpressionExt{Flags: scankit.ExtEditDistance, EditDistance: 1},
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, data := range [][]byte{
		[]byte("AAAAAAA"),
		[]byte("AAAAAA"),
		[]byte("AAAAAAAA"),
		[]byte("AABBAQBA"),
		[]byte("prefix=ABABABA suffix"),
	} {
		got, err := product.Scan(data)
		if err != nil {
			t.Fatal(err)
		}
		want, err := reference.Scan(data)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != len(want) {
			t.Fatalf("Scan(%q) match count = %d, want %d; got=%#v want=%#v", data, len(got), len(want), got, want)
		}
		for index := range got {
			if got[index].From != want[index].From || got[index].To != want[index].To {
				t.Fatalf("Scan(%q) range[%d] = [%d,%d), want [%d,%d)", data, index, got[index].From, got[index].To, want[index].From, want[index].To)
			}
		}
	}
}

func TestEditDistanceMatchesBoundedVariableWidthByteNFAProduct(t *testing.T) {
	t.Parallel()
	db, err := scankit.Compile([]scankit.Expression{{
		Id:      1,
		Pattern: `(?:AB|CD){5,7}`,
		Ext:     &scankit.ExpressionExt{Flags: scankit.ExtEditDistance, EditDistance: 1},
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name string
		data string
		want []scankit.Match
	}{
		{name: "exact_and_deletion", data: "ABABABABAB", want: []scankit.Match{{Id: 1, From: 0, To: 9}, {Id: 1, From: 0, To: 10}}},
		{name: "insertion", data: "ABABABABABQ", want: []scankit.Match{{Id: 1, From: 0, To: 9}, {Id: 1, From: 0, To: 10}, {Id: 1, From: 0, To: 11}}},
		{name: "two_substitutions", data: "ABABABABQQ", want: nil},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := db.Scan([]byte(test.data))
			if err != nil {
				t.Fatal(err)
			}
			assertMatchesEqual(t, got, test.want)
		})
	}
}

func TestEditNFAProductRejectsUnboundedResourceOrLanguageShapes(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name     string
		pattern  string
		distance uint32
	}{
		{name: "unbounded_width", pattern: `(?:A|B){1,}`, distance: 1},
		{name: "width_limit", pattern: `(?:A|B){257}`, distance: 1},
		{name: "distance_limit", pattern: `(?:A|B){9}`, distance: 65},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := scankit.Compile([]scankit.Expression{{
				Id:      1,
				Pattern: test.pattern,
				Ext:     &scankit.ExpressionExt{Flags: scankit.ExtEditDistance, EditDistance: test.distance},
			}})
			if !errors.Is(err, scankit.ErrUnsupportedExtension) {
				t.Fatalf("Compile(%q) error = %v, want ErrUnsupportedExtension", test.pattern, err)
			}
		})
	}
}

func TestUCPBoundedGraphApproximateMatchesFixedClassReference(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		ext  scankit.ExpressionExt
		data []byte
	}{
		{name: "hamming", ext: scankit.ExpressionExt{Flags: scankit.ExtHammingDistance, HammingDistance: 1}, data: []byte("张0张0张0文")},
		{name: "edit", ext: scankit.ExpressionExt{Flags: scankit.ExtEditDistance, EditDistance: 1}, data: []byte("张0张0张0文0")},
	} {
		t.Run(test.name, func(t *testing.T) {
			product, err := scankit.Compile([]scankit.Expression{{
				Id:      1,
				Pattern: `(?:张|0){7}`,
				Flags:   scankit.CompileUTF8 | scankit.CompileUnicodeProperties,
				Ext:     &test.ext,
			}})
			if err != nil {
				t.Fatal(err)
			}
			reference, err := scankit.Compile([]scankit.Expression{{
				Id:      2,
				Pattern: `[张0]{7}`,
				Flags:   scankit.CompileUTF8 | scankit.CompileUnicodeProperties,
				Ext:     &test.ext,
			}})
			if err != nil {
				t.Fatal(err)
			}
			got, err := product.Scan(test.data)
			if err != nil {
				t.Fatal(err)
			}
			want, err := reference.Scan(test.data)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(want) {
				t.Fatalf("match count = %d, want %d; got=%#v want=%#v", len(got), len(want), got, want)
			}
			for index := range got {
				if got[index].From != want[index].From || got[index].To != want[index].To {
					t.Fatalf("range[%d] = [%d,%d), want [%d,%d)", index, got[index].From, got[index].To, want[index].From, want[index].To)
				}
			}
		})
	}
}

func TestEditDistanceMatchesBoundedVariableWidthUCPGraphProduct(t *testing.T) {
	t.Parallel()
	product, err := scankit.Compile([]scankit.Expression{{
		Id:      1,
		Pattern: `(?:张|0){6,8}`,
		Flags:   scankit.CompileUTF8 | scankit.CompileUnicodeProperties,
		Ext:     &scankit.ExpressionExt{Flags: scankit.ExtEditDistance, EditDistance: 1},
	}})
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("服务=张0张0张文0 status=张0张0张0张0")
	got, err := product.Scan(data)
	if err != nil {
		t.Fatal(err)
	}
	want := approximateFixedUCPReferenceRanges(t, data, 6, 8)
	if actual := approximateRanges(got); !equalApproximateRanges(actual, want) {
		t.Fatalf("variable-width UCP graph ranges = %#v, want %#v", actual, want)
	}
}

func TestUCPVariableWidthApproximateGraphRejectsUnsupportedDistanceShapes(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name  string
		regex string
		ext   scankit.ExpressionExt
	}{
		{name: "hamming_requires_fixed_width", regex: `(?:张|0){6,8}`, ext: scankit.ExpressionExt{Flags: scankit.ExtHammingDistance, HammingDistance: 1}},
		{name: "edit_rejects_unbounded_width", regex: `(?:张|0){6,}`, ext: scankit.ExpressionExt{Flags: scankit.ExtEditDistance, EditDistance: 1}},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := scankit.Compile([]scankit.Expression{{
				Id:      1,
				Pattern: test.regex,
				Flags:   scankit.CompileUTF8 | scankit.CompileUnicodeProperties,
				Ext:     &test.ext,
			}})
			if !errors.Is(err, scankit.ErrUnsupportedExtension) {
				t.Fatalf("Compile(%q) error = %v, want ErrUnsupportedExtension", test.regex, err)
			}
		})
	}
}

func approximateFixedByteReferenceRanges(t *testing.T, data []byte, minimum, maximum int) map[[2]uint64]struct{} {
	t.Helper()
	leftmostByEnd := make(map[uint64]uint64)
	for width := minimum; width <= maximum; width++ {
		database, err := scankit.Compile([]scankit.Expression{{
			Id:      2,
			Pattern: strings.Repeat(`[AB]`, width),
			Ext:     &scankit.ExpressionExt{Flags: scankit.ExtEditDistance, EditDistance: 1},
		}})
		if err != nil {
			t.Fatal(err)
		}
		matches, err := database.Scan(data)
		if err != nil {
			t.Fatal(err)
		}
		for _, match := range matches {
			if existing, ok := leftmostByEnd[match.To]; !ok || match.From < existing {
				leftmostByEnd[match.To] = match.From
			}
		}
	}
	ranges := make(map[[2]uint64]struct{}, len(leftmostByEnd))
	for end, start := range leftmostByEnd {
		ranges[[2]uint64{start, end}] = struct{}{}
	}
	return ranges
}

func approximateFixedUCPReferenceRanges(t *testing.T, data []byte, minimum, maximum int) map[[2]uint64]struct{} {
	t.Helper()
	leftmostByEnd := make(map[uint64]uint64)
	for width := minimum; width <= maximum; width++ {
		database, err := scankit.Compile([]scankit.Expression{{
			Id:      2,
			Pattern: strings.Repeat(`[张0]`, width),
			Flags:   scankit.CompileUTF8 | scankit.CompileUnicodeProperties,
			Ext:     &scankit.ExpressionExt{Flags: scankit.ExtEditDistance, EditDistance: 1},
		}})
		if err != nil {
			t.Fatal(err)
		}
		matches, err := database.Scan(data)
		if err != nil {
			t.Fatal(err)
		}
		for _, match := range matches {
			if existing, ok := leftmostByEnd[match.To]; !ok || match.From < existing {
				leftmostByEnd[match.To] = match.From
			}
		}
	}
	ranges := make(map[[2]uint64]struct{}, len(leftmostByEnd))
	for end, start := range leftmostByEnd {
		ranges[[2]uint64{start, end}] = struct{}{}
	}
	return ranges
}

func approximateRanges(matches []scankit.Match) map[[2]uint64]struct{} {
	ranges := make(map[[2]uint64]struct{}, len(matches))
	for _, match := range matches {
		ranges[[2]uint64{match.From, match.To}] = struct{}{}
	}
	return ranges
}

func equalApproximateRanges(left, right map[[2]uint64]struct{}) bool {
	if len(left) != len(right) {
		return false
	}
	for value := range left {
		if _, ok := right[value]; !ok {
			return false
		}
	}
	return true
}

func TestEditDistanceMatchesLiteralInsertionsDeletionsAndSubstitutions(t *testing.T) {
	t.Parallel()
	db, err := scankit.Compile([]scankit.Expression{{
		Id:      1,
		Pattern: "ab",
		Ext:     &scankit.ExpressionExt{Flags: scankit.ExtEditDistance, EditDistance: 1},
	}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := db.Scan([]byte("abc"))
	if err != nil {
		t.Fatal(err)
	}
	assertMatchesEqual(t, got, []scankit.Match{
		{Id: 1, From: 0, To: 1}, // deletion of b
		{Id: 1, From: 1, To: 2}, // deletion of a
		{Id: 1, From: 0, To: 2}, // exact
		{Id: 1, From: 0, To: 3}, // insertion of c
	})
}

func TestEditDistanceMatchesFixedWidthByteRegex(t *testing.T) {
	t.Parallel()
	db, err := scankit.Compile([]scankit.Expression{{
		Id:      1,
		Pattern: `ID:\d{2}`,
		Ext:     &scankit.ExpressionExt{Flags: scankit.ExtEditDistance, EditDistance: 1},
	}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := db.Scan([]byte("ID:42 ID:4 IX:42"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 {
		t.Fatal("expected approximate fixed regex matches")
	}
	for _, match := range got {
		if match.Id != 1 || match.To > uint64(len("ID:42 ID:4 IX:42")) {
			t.Fatalf("invalid match %#v", match)
		}
	}
}

func FuzzFixedWidthByteRegexApproximateScanIntoMatchesScan(f *testing.F) {
	f.Add([]byte("ID:42 ID:4x IX:4x AB12 CD34 AX12"))
	f.Add([]byte("AAAAAAAAA AAAAAAAAQ AAAAAAAQQ"))
	f.Add([]byte(""))
	f.Add([]byte{0x00, 0xff, 'I', 'D', ':', '4', '2'})
	database, err := scankit.Compile([]scankit.Expression{
		{Id: 1, Pattern: `ID:\d{2}`, Ext: &scankit.ExpressionExt{Flags: scankit.ExtHammingDistance, HammingDistance: 1}},
		{Id: 2, Pattern: `(?:AB|CD)[0-9]{2}`, Ext: &scankit.ExpressionExt{Flags: scankit.ExtHammingDistance, HammingDistance: 1}},
		{Id: 3, Pattern: `ID:\d{2}`, Ext: &scankit.ExpressionExt{Flags: scankit.ExtEditDistance, EditDistance: 1}},
		{Id: 4, Pattern: `(?:AB|CD)[0-9]{2}`, Ext: &scankit.ExpressionExt{Flags: scankit.ExtEditDistance, EditDistance: 1}},
		{Id: 5, Pattern: `(?:A|B){9}`, Ext: &scankit.ExpressionExt{Flags: scankit.ExtHammingDistance, HammingDistance: 1}},
		{Id: 6, Pattern: `(?:A|B){9}`, Ext: &scankit.ExpressionExt{Flags: scankit.ExtEditDistance, EditDistance: 1}},
	})
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 4_096 {
			t.Skip()
		}
		want, err := database.Scan(data)
		if err != nil {
			t.Fatal(err)
		}
		got, err := database.ScanInto(data, make([]scankit.Match, 0, len(want)))
		if err != nil {
			t.Fatal(err)
		}
		assertMatchesEqual(t, got, want)
		for _, match := range got {
			if match.From >= match.To || match.To > uint64(len(data)) {
				t.Fatalf("invalid approximate range %#v for %q", match, data)
			}
		}
	})
}

func FuzzBoundedFixedWidthByteClassRepeatEditMatchesFixedReference(f *testing.F) {
	f.Add([]byte("ABABABA"))
	f.Add([]byte("ABABAB"))
	f.Add([]byte("ABABABAQ"))
	f.Add([]byte{0x00, 'A', 'B', 'A', 'B', 'A', 'B', 'A', 0xff})
	product, err := scankit.Compile([]scankit.Expression{{
		Id:      1,
		Pattern: `(?:A|B){7}`,
		Ext:     &scankit.ExpressionExt{Flags: scankit.ExtEditDistance, EditDistance: 1},
	}})
	if err != nil {
		f.Fatal(err)
	}
	reference, err := scankit.Compile([]scankit.Expression{{
		Id:      2,
		Pattern: `[AB]{7}`,
		Ext:     &scankit.ExpressionExt{Flags: scankit.ExtEditDistance, EditDistance: 1},
	}})
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1_024 {
			t.Skip()
		}
		got, err := product.Scan(data)
		if err != nil {
			t.Fatal(err)
		}
		want, err := reference.Scan(data)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != len(want) {
			t.Fatalf("Scan(%q) match count = %d, want %d", data, len(got), len(want))
		}
		for index := range got {
			if got[index].From != want[index].From || got[index].To != want[index].To {
				t.Fatalf("Scan(%q) range[%d] = [%d,%d), want [%d,%d)", data, index, got[index].From, got[index].To, want[index].From, want[index].To)
			}
		}
	})
}

func FuzzBoundedVariableWidthNFAEditScanIntoMatchesScan(f *testing.F) {
	f.Add([]byte("ABABABABAB"))
	f.Add([]byte("ABABABABABQ"))
	f.Add([]byte{0x00, 'A', 'B', 'A', 'B', 'C', 'D', 0xff})
	database, err := scankit.Compile([]scankit.Expression{{
		Id:      1,
		Pattern: `(?:AB|CD){5,7}`,
		Ext:     &scankit.ExpressionExt{Flags: scankit.ExtEditDistance, EditDistance: 1},
	}})
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1_024 {
			t.Skip()
		}
		want, err := database.Scan(data)
		if err != nil {
			t.Fatal(err)
		}
		got, err := database.ScanInto(data, make([]scankit.Match, 0, len(want)))
		if err != nil {
			t.Fatal(err)
		}
		assertMatchesEqual(t, got, want)
	})
}

func FuzzBoundedVariableWidthClassRepeatEditMatchesFixedReference(f *testing.F) {
	f.Add([]byte("ABABABA"))
	f.Add([]byte("ABABABAQ"))
	f.Add([]byte{0x00, 'A', 'B', 'A', 'B', 'A', 'B', 0xff})
	lowered, err := scankit.Compile([]scankit.Expression{{
		Id:      1,
		Pattern: `(?:A|B){6,8}`,
		Ext:     &scankit.ExpressionExt{Flags: scankit.ExtEditDistance, EditDistance: 1},
	}})
	if err != nil {
		f.Fatal(err)
	}
	reference, err := scankit.Compile([]scankit.Expression{{
		Id:      2,
		Pattern: `(?:[AB]{6}|[AB]{7}|[AB]{8})`,
		Ext:     &scankit.ExpressionExt{Flags: scankit.ExtEditDistance, EditDistance: 1},
	}})
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 512 {
			t.Skip()
		}
		got, err := lowered.Scan(data)
		if err != nil {
			t.Fatal(err)
		}
		want, err := reference.Scan(data)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != len(want) {
			t.Fatalf("Scan(%q) match count = %d, want %d", data, len(got), len(want))
		}
		for index := range got {
			if got[index].From != want[index].From || got[index].To != want[index].To {
				t.Fatalf("Scan(%q) range[%d] = [%d,%d), want [%d,%d)", data, index, got[index].From, got[index].To, want[index].From, want[index].To)
			}
		}
	})
}

func FuzzUCPBoundedGraphApproximateMatchesFixedReference(f *testing.F) {
	f.Add([]byte("张0张0张0文0"))
	f.Add([]byte("0000000"))
	f.Add([]byte{0xff, '0', 0xe5})
	product, err := scankit.Compile([]scankit.Expression{{
		Id:      1,
		Pattern: `(?:张|0){7}`,
		Flags:   scankit.CompileUTF8 | scankit.CompileUnicodeProperties,
		Ext:     &scankit.ExpressionExt{Flags: scankit.ExtEditDistance, EditDistance: 1},
	}})
	if err != nil {
		f.Fatal(err)
	}
	reference, err := scankit.Compile([]scankit.Expression{{
		Id:      2,
		Pattern: `[张0]{7}`,
		Flags:   scankit.CompileUTF8 | scankit.CompileUnicodeProperties,
		Ext:     &scankit.ExpressionExt{Flags: scankit.ExtEditDistance, EditDistance: 1},
	}})
	if err != nil {
		f.Fatal(err)
	}
	hammingProduct, err := scankit.Compile([]scankit.Expression{{
		Id:      3,
		Pattern: `(?:张|0){7}`,
		Flags:   scankit.CompileUTF8 | scankit.CompileUnicodeProperties,
		Ext:     &scankit.ExpressionExt{Flags: scankit.ExtHammingDistance, HammingDistance: 1},
	}})
	if err != nil {
		f.Fatal(err)
	}
	hammingReference, err := scankit.Compile([]scankit.Expression{{
		Id:      4,
		Pattern: `[张0]{7}`,
		Flags:   scankit.CompileUTF8 | scankit.CompileUnicodeProperties,
		Ext:     &scankit.ExpressionExt{Flags: scankit.ExtHammingDistance, HammingDistance: 1},
	}})
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1_024 {
			t.Skip()
		}
		got, productErr := product.Scan(data)
		want, referenceErr := reference.Scan(data)
		if productErr != nil || referenceErr != nil {
			if !errors.Is(productErr, scankit.ErrInvalidUTF8) || !errors.Is(referenceErr, scankit.ErrInvalidUTF8) {
				t.Fatalf("Scan() errors = %v / %v, want matching ErrInvalidUTF8", productErr, referenceErr)
			}
			return
		}
		if len(got) != len(want) {
			t.Fatalf("Scan(%q) match count = %d, want %d", data, len(got), len(want))
		}
		for index := range got {
			if got[index].From != want[index].From || got[index].To != want[index].To {
				t.Fatalf("Scan(%q) range[%d] = [%d,%d), want [%d,%d)", data, index, got[index].From, got[index].To, want[index].From, want[index].To)
			}
		}
		got, productErr = hammingProduct.Scan(data)
		want, referenceErr = hammingReference.Scan(data)
		if productErr != nil || referenceErr != nil {
			if !errors.Is(productErr, scankit.ErrInvalidUTF8) || !errors.Is(referenceErr, scankit.ErrInvalidUTF8) {
				t.Fatalf("Hamming Scan() errors = %v / %v, want matching ErrInvalidUTF8", productErr, referenceErr)
			}
			return
		}
		if len(got) != len(want) {
			t.Fatalf("Hamming Scan(%q) match count = %d, want %d", data, len(got), len(want))
		}
		for index := range got {
			if got[index].From != want[index].From || got[index].To != want[index].To {
				t.Fatalf("Hamming Scan(%q) range[%d] = [%d,%d), want [%d,%d)", data, index, got[index].From, got[index].To, want[index].From, want[index].To)
			}
		}
	})
}

func FuzzBoundedVariableWidthUCPGraphEditScanIntoMatchesScan(f *testing.F) {
	f.Add([]byte("张0张0张文0"))
	f.Add([]byte("00000000"))
	f.Add([]byte{0xff, '0', 0xe5})
	database, err := scankit.Compile([]scankit.Expression{{
		Id:      1,
		Pattern: `(?:张|0){6,8}`,
		Flags:   scankit.CompileUTF8 | scankit.CompileUnicodeProperties,
		Ext:     &scankit.ExpressionExt{Flags: scankit.ExtEditDistance, EditDistance: 1},
	}})
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 512 {
			t.Skip()
		}
		want, err := database.Scan(data)
		if !utf8.Valid(data) {
			if !errors.Is(err, scankit.ErrInvalidUTF8) {
				t.Fatalf("Scan() error = %v, want ErrInvalidUTF8", err)
			}
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		got, err := database.ScanInto(data, make([]scankit.Match, 0, len(want)))
		if err != nil {
			t.Fatal(err)
		}
		assertMatchesEqual(t, got, want)
	})
}

func TestUTF8LiteralMatchesOnlyValidUTF8Input(t *testing.T) {
	t.Parallel()
	db, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: "张三", Flags: scankit.CompileUTF8}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := db.Scan([]byte("姓名张三"))
	if err != nil {
		t.Fatal(err)
	}
	assertMatchesEqual(t, got, []scankit.Match{{Id: 1, From: 6, To: 12}})
	if _, err := db.Scan([]byte{'\xe5', 'x'}); !errors.Is(err, scankit.ErrInvalidUTF8) {
		t.Fatalf("Scan() error = %v, want ErrInvalidUTF8", err)
	}
}

func TestUCPLiteralRequiresUTF8AndUsesExactUTF8Matching(t *testing.T) {
	t.Parallel()
	if _, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: "张三", Flags: scankit.CompileUnicodeProperties}}); !errors.Is(err, scankit.ErrUnsupportedFlag) {
		t.Fatalf("Compile() error = %v, want ErrUnsupportedFlag", err)
	}
	db, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: "张三", Flags: scankit.CompileUTF8 | scankit.CompileUnicodeProperties}})
	if err != nil {
		t.Fatal(err)
	}
	matches, err := db.Scan([]byte("姓名张三"))
	if err != nil {
		t.Fatal(err)
	}
	assertMatchesEqual(t, matches, []scankit.Match{{Id: 1, From: 6, To: 12}})
	if _, err := db.Scan([]byte{'\xe5', 'x'}); !errors.Is(err, scankit.ErrInvalidUTF8) {
		t.Fatalf("Scan() error = %v, want ErrInvalidUTF8", err)
	}
}

func TestUTF8CaselessASCIIOnly(t *testing.T) {
	t.Parallel()
	db, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: "Token", Flags: scankit.CompileUTF8 | scankit.CompileCaseless}})
	if err != nil {
		t.Fatal(err)
	}
	matches, err := db.Scan([]byte("TOKEN token"))
	if err != nil {
		t.Fatal(err)
	}
	assertMatchesEqual(t, matches, []scankit.Match{{Id: 1, From: 0, To: 5}, {Id: 1, From: 6, To: 11}})
	if _, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: "张三", Flags: scankit.CompileUTF8 | scankit.CompileCaseless}}); !errors.Is(err, scankit.ErrUnsupportedFlag) {
		t.Fatalf("Compile() error = %v, want ErrUnsupportedFlag", err)
	}
}

func TestUCPPropertyRulesScanUTF8Runes(t *testing.T) {
	t.Parallel()
	db, err := scankit.Compile([]scankit.Expression{
		{Id: 1, Pattern: `\p{Han}+`, Flags: scankit.CompileUTF8 | scankit.CompileUnicodeProperties},
		{Id: 2, Pattern: `\p{Nd}{2}`, Flags: scankit.CompileUTF8 | scankit.CompileUnicodeProperties},
		{Id: 3, Pattern: `\p{L}\p{Nd}`, Flags: scankit.CompileUTF8 | scankit.CompileUnicodeProperties},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := db.Scan([]byte("A张三B李１２34"))
	if err != nil {
		t.Fatal(err)
	}
	assertMatchesEqual(t, got, []scankit.Match{
		{Id: 1, From: 1, To: 4},
		{Id: 1, From: 1, To: 7},
		{Id: 1, From: 4, To: 7},
		{Id: 1, From: 8, To: 11},
		{Id: 3, From: 8, To: 14},
		{Id: 2, From: 11, To: 17},
		{Id: 2, From: 14, To: 18},
		{Id: 2, From: 17, To: 19},
	})
}

func TestUCPPropertyRuleSupportsNegationAndReportingFlags(t *testing.T) {
	t.Parallel()
	db, err := scankit.Compile([]scankit.Expression{{
		Id:      1,
		Pattern: `\P{Han}+`,
		Flags:   scankit.CompileUTF8 | scankit.CompileUnicodeProperties | scankit.CompileLeftmostStart | scankit.CompileSingleMatch,
	}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := db.Scan([]byte("张AB三C"))
	if err != nil {
		t.Fatal(err)
	}
	assertMatchesEqual(t, got, []scankit.Match{{Id: 1, From: 3, To: 4}})
}

func TestUCPShorthandsAndPropertyClasses(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		pattern string
		data    string
		want    []scankit.Match
	}{
		{
			name:    "decimal number",
			pattern: `\d+`,
			data:    "A１２B",
			want:    []scankit.Match{{Id: 1, From: 1, To: 4}, {Id: 1, From: 1, To: 7}, {Id: 1, From: 4, To: 7}},
		},
		{
			name:    "unicode whitespace",
			pattern: `\s+`,
			data:    "A　B",
			want:    []scankit.Match{{Id: 1, From: 1, To: 4}},
		},
		{
			name:    "unicode word",
			pattern: `\w{3}`,
			data:    "A１２!",
			want:    []scankit.Match{{Id: 1, From: 0, To: 7}},
		},
		{
			name:    "property class union",
			pattern: `[\p{Han}\d]{2}`,
			data:    "A张１B",
			want:    []scankit.Match{{Id: 1, From: 1, To: 7}},
		},
		{
			name:    "property class literals",
			pattern: `[A_\p{Han}]{2}`,
			data:    "xA张y",
			want:    []scankit.Match{{Id: 1, From: 1, To: 5}},
		},
		{
			name:    "Unicode code point ranges",
			pattern: `[A-C一-丂]{2}`,
			data:    "xB丁y",
			want:    []scankit.Match{{Id: 1, From: 1, To: 5}},
		},
		{
			name:    "negated property class",
			pattern: `[^\p{Han}\d]+`,
			data:    "张A　１",
			want:    []scankit.Match{{Id: 1, From: 3, To: 4}, {Id: 1, From: 3, To: 7}, {Id: 1, From: 4, To: 7}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: test.pattern, Flags: scankit.CompileUTF8 | scankit.CompileUnicodeProperties}})
			if err != nil {
				t.Fatal(err)
			}
			got, err := db.Scan([]byte(test.data))
			if err != nil {
				t.Fatal(err)
			}
			assertMatchesEqual(t, got, test.want)
		})
	}
}

func TestUCPPOSIXCharacterClasses(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		pattern string
		data    string
		want    []scankit.Match
	}{
		{
			name:    "alpha uses Unicode letters",
			pattern: `[[:alpha:]]+`,
			data:    "A张3",
			want:    []scankit.Match{{Id: 1, From: 0, To: 1}, {Id: 1, From: 0, To: 4}},
		},
		{
			name:    "digit uses Unicode decimal numbers",
			pattern: `[[:digit:]]{2}`,
			data:    "A１２B",
			want:    []scankit.Match{{Id: 1, From: 1, To: 7}},
		},
		{
			name:    "space includes Unicode whitespace",
			pattern: `[[:space:]]+`,
			data:    "A　\tB",
			want:    []scankit.Match{{Id: 1, From: 1, To: 4}, {Id: 1, From: 1, To: 5}},
		},
		{
			name:    "blank excludes vertical whitespace",
			pattern: `[[:blank:]]+`,
			data:    "\u00a0　\n",
			want:    []scankit.Match{{Id: 1, From: 0, To: 2}, {Id: 1, From: 0, To: 5}},
		},
		{
			name:    "word is Unicode aware",
			pattern: `[[:word:]]{2}`,
			data:    "张１!",
			want:    []scankit.Match{{Id: 1, From: 0, To: 6}},
		},
		{
			name:    "inner negation",
			pattern: `[[:^digit:]]+`,
			data:    "A１B",
			want:    []scankit.Match{{Id: 1, From: 0, To: 1}, {Id: 1, From: 4, To: 5}},
		},
		{
			name:    "outer negation",
			pattern: `[^[:digit:]]+`,
			data:    "A１B",
			want:    []scankit.Match{{Id: 1, From: 0, To: 1}, {Id: 1, From: 4, To: 5}},
		},
		{
			name:    "class union in graph and scoped caseless",
			pattern: `(?i:[[:upper:]][[:digit:]])`,
			data:    "σ１",
			want:    []scankit.Match{{Id: 1, From: 0, To: 5}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			scanner, err := scankit.Compile([]scankit.Expression{{
				Id:      1,
				Pattern: test.pattern,
				Flags:   scankit.CompileUTF8 | scankit.CompileUnicodeProperties,
			}})
			if err != nil {
				t.Fatal(err)
			}
			matches, err := scanner.Scan([]byte(test.data))
			if err != nil {
				t.Fatal(err)
			}
			assertMatchesEqual(t, matches, test.want)
		})
	}
}

func TestUCPPOSIXCharacterClassesRejectInvalidSyntax(t *testing.T) {
	t.Parallel()
	for _, pattern := range []string{`[[:unknown:]]`, `[[:^unknown:]]`, `[[:DIGIT:]]`, `[[:digit]`, `[[:^:]]`, `[[:digit:]`} {
		_, err := scankit.Compile([]scankit.Expression{{
			Id:      1,
			Pattern: pattern,
			Flags:   scankit.CompileUTF8 | scankit.CompileUnicodeProperties,
		}})
		if !errors.Is(err, scankit.ErrUnsupportedExpression) {
			t.Fatalf("Compile(%q) error = %v, want ErrUnsupportedExpression", pattern, err)
		}
	}
}

func TestUCPPOSIXCharacterClassMembership(t *testing.T) {
	t.Parallel()
	const data = "Aa张１_!　\n\x01"
	tests := []struct {
		name string
		want [][2]uint64
	}{
		{name: "alnum", want: [][2]uint64{{0, 1}, {1, 2}, {2, 5}, {5, 8}}},
		{name: "alpha", want: [][2]uint64{{0, 1}, {1, 2}, {2, 5}}},
		{name: "ascii", want: [][2]uint64{{0, 1}, {1, 2}, {8, 9}, {9, 10}, {13, 14}, {14, 15}}},
		{name: "blank", want: [][2]uint64{{10, 13}}},
		{name: "cntrl", want: [][2]uint64{{13, 14}, {14, 15}}},
		{name: "digit", want: [][2]uint64{{5, 8}}},
		{name: "graph", want: [][2]uint64{{0, 1}, {1, 2}, {2, 5}, {5, 8}, {8, 9}, {9, 10}}},
		{name: "lower", want: [][2]uint64{{1, 2}}},
		{name: "print", want: [][2]uint64{{0, 1}, {1, 2}, {2, 5}, {5, 8}, {8, 9}, {9, 10}, {10, 13}}},
		{name: "punct", want: [][2]uint64{{8, 9}, {9, 10}}},
		{name: "space", want: [][2]uint64{{10, 13}, {13, 14}}},
		{name: "upper", want: [][2]uint64{{0, 1}}},
		{name: "word", want: [][2]uint64{{0, 1}, {1, 2}, {2, 5}, {5, 8}, {8, 9}}},
		{name: "xdigit", want: [][2]uint64{{0, 1}, {1, 2}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			scanner, err := scankit.Compile([]scankit.Expression{{
				Id:      1,
				Pattern: `[[:` + test.name + `:]]`,
				Flags:   scankit.CompileUTF8 | scankit.CompileUnicodeProperties,
			}})
			if err != nil {
				t.Fatal(err)
			}
			matches, err := scanner.Scan([]byte(data))
			if err != nil {
				t.Fatal(err)
			}
			want := make([]scankit.Match, len(test.want))
			for index, span := range test.want {
				want[index] = scankit.Match{Id: 1, From: span[0], To: span[1]}
			}
			assertMatchesEqual(t, matches, want)
		})
	}
}

func FuzzUCPPOSIXCharacterClassesReportValidRuneRanges(f *testing.F) {
	f.Add([]byte("张A１２　_!Σ１\n"))
	f.Add([]byte(""))
	f.Add([]byte{'A', 0xff})

	scanner, err := scankit.Compile([]scankit.Expression{
		{Id: 1, Pattern: `[[:alpha:]]+`, Flags: scankit.CompileUTF8 | scankit.CompileUnicodeProperties},
		{Id: 2, Pattern: `[[:digit:]]{2}`, Flags: scankit.CompileUTF8 | scankit.CompileUnicodeProperties},
		{Id: 3, Pattern: `[[:space:]]+`, Flags: scankit.CompileUTF8 | scankit.CompileUnicodeProperties},
		{Id: 4, Pattern: `[[:blank:]]+`, Flags: scankit.CompileUTF8 | scankit.CompileUnicodeProperties},
		{Id: 5, Pattern: `[[:word:]]{2}`, Flags: scankit.CompileUTF8 | scankit.CompileUnicodeProperties},
		{Id: 6, Pattern: `[[:^digit:]]+`, Flags: scankit.CompileUTF8 | scankit.CompileUnicodeProperties},
		{Id: 7, Pattern: `(?i:[[:upper:]][[:digit:]])`, Flags: scankit.CompileUTF8 | scankit.CompileUnicodeProperties},
	})
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 4_096 {
			t.Skip()
		}
		matches, err := scanner.Scan(data)
		if !utf8.Valid(data) {
			if !errors.Is(err, scankit.ErrInvalidUTF8) {
				t.Fatalf("Scan() error = %v, want ErrInvalidUTF8", err)
			}
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		into, err := scanner.ScanInto(data, make([]scankit.Match, 0, len(matches)))
		if err != nil {
			t.Fatal(err)
		}
		assertMatchesEqual(t, into, matches)
		for _, match := range matches {
			from, to := int(match.From), int(match.To)
			if from >= to || to > len(data) || !utf8.RuneStart(data[from]) || to < len(data) && !utf8.RuneStart(data[to]) {
				t.Fatalf("invalid POSIX rune range %#v for %q", match, data)
			}
			values := []rune(string(data[from:to]))
			switch match.Id {
			case 1:
				for _, value := range values {
					if !unicode.IsLetter(value) {
						t.Fatalf("alpha match %#v contains %U", match, value)
					}
				}
			case 2:
				if len(values) != 2 {
					t.Fatalf("digit match %#v has %d runes, want 2", match, len(values))
				}
				for _, value := range values {
					if !unicode.Is(unicode.Nd, value) {
						t.Fatalf("digit match %#v contains %U", match, value)
					}
				}
			case 3:
				for _, value := range values {
					if !unicode.IsSpace(value) {
						t.Fatalf("space match %#v contains %U", match, value)
					}
				}
			case 4:
				for _, value := range values {
					if !isUCPHorizontalSpace(value) {
						t.Fatalf("blank match %#v contains %U", match, value)
					}
				}
			case 5:
				if len(values) != 2 {
					t.Fatalf("word match %#v has %d runes, want 2", match, len(values))
				}
				for _, value := range values {
					if !isUCPWordRune(value) {
						t.Fatalf("word match %#v contains %U", match, value)
					}
				}
			case 6:
				for _, value := range values {
					if unicode.Is(unicode.Nd, value) {
						t.Fatalf("negated digit match %#v contains %U", match, value)
					}
				}
			case 7:
				if len(values) != 2 || !unicodeSimpleFoldContainsUpper(values[0]) || !unicode.Is(unicode.Nd, values[1]) {
					t.Fatalf("caseless upper-plus-digit match %#v is invalid", match)
				}
			default:
				t.Fatalf("unexpected expression ID %d", match.Id)
			}
		}
	})
}

func TestUCPPropertyConcatenationSupportsPositiveRepeats(t *testing.T) {
	t.Parallel()
	tests := []struct {
		pattern string
		data    string
		want    []scankit.Match
	}{
		{
			pattern: `\p{L}+\p{Nd}`,
			data:    "AB１２C3",
			want:    []scankit.Match{{Id: 1, From: 0, To: 5}, {Id: 1, From: 1, To: 5}, {Id: 1, From: 8, To: 10}},
		},
		{
			pattern: `[\p{Han}\d]{1,3}\s`,
			data:    "张１２ ",
			want:    []scankit.Match{{Id: 1, From: 0, To: 10}, {Id: 1, From: 3, To: 10}, {Id: 1, From: 6, To: 10}},
		},
	}
	for _, test := range tests {
		db, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: test.pattern, Flags: scankit.CompileUTF8 | scankit.CompileUnicodeProperties}})
		if err != nil {
			t.Fatal(err)
		}
		got, err := db.Scan([]byte(test.data))
		if err != nil {
			t.Fatal(err)
		}
		assertMatchesEqual(t, got, test.want)
	}
}

func TestUCPPropertyRulesSupportUnicodeAndASCIILiterals(t *testing.T) {
	t.Parallel()
	db, err := scankit.Compile([]scankit.Expression{{
		Id:      1,
		Pattern: `ID:\p{Nd}{2}`,
		Flags:   scankit.CompileUTF8 | scankit.CompileUnicodeProperties,
	}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := db.Scan([]byte("xID:１２ yID:34"))
	if err != nil {
		t.Fatal(err)
	}
	assertMatchesEqual(t, got, []scankit.Match{{Id: 1, From: 1, To: 10}, {Id: 1, From: 12, To: 17}})
}

func TestUCPPropertyRulesSupportTopLevelAlternation(t *testing.T) {
	t.Parallel()
	db, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: `\p{Han}+|\d{2}`, Flags: scankit.CompileUTF8 | scankit.CompileUnicodeProperties}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := db.Scan([]byte("张１２3"))
	if err != nil {
		t.Fatal(err)
	}
	assertMatchesEqual(t, got, []scankit.Match{{Id: 1, From: 0, To: 3}, {Id: 1, From: 3, To: 9}, {Id: 1, From: 6, To: 10}})
}

func TestUCPAlternationDoesNotDuplicateOverlappingRanges(t *testing.T) {
	t.Parallel()
	db, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: `\p{L}|\w`, Flags: scankit.CompileUTF8 | scankit.CompileUnicodeProperties}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := db.Scan([]byte("A"))
	if err != nil {
		t.Fatal(err)
	}
	assertMatchesEqual(t, got, []scankit.Match{{Id: 1, From: 0, To: 1}})
}

func TestUCPPropertyRulesSupportNestedGroups(t *testing.T) {
	t.Parallel()
	db, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: `(?:ID:|(?:张:|名:))\d{2}`, Flags: scankit.CompileUTF8 | scankit.CompileUnicodeProperties}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := db.Scan([]byte("ID:１２ 名:34"))
	if err != nil {
		t.Fatal(err)
	}
	assertMatchesEqual(t, got, []scankit.Match{{Id: 1, From: 0, To: 9}, {Id: 1, From: 10, To: 16}})
}

func TestUCPPropertyGroupsSupportRepeatedNestedAlternation(t *testing.T) {
	t.Parallel()
	db, err := scankit.Compile([]scankit.Expression{
		{Id: 1, Pattern: `(\p{L}|\d)+`, Flags: scankit.CompileUTF8 | scankit.CompileUnicodeProperties},
		{Id: 2, Pattern: `(?:(?:ID:|\p{Han}:)){2}`, Flags: scankit.CompileUTF8 | scankit.CompileUnicodeProperties},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := db.Scan([]byte("A１B2! xID:张:y"))
	if err != nil {
		t.Fatal(err)
	}
	assertMatchesEqual(t, got, []scankit.Match{
		{Id: 1, From: 0, To: 1},
		{Id: 1, From: 0, To: 4},
		{Id: 1, From: 1, To: 4},
		{Id: 1, From: 0, To: 5},
		{Id: 1, From: 1, To: 5},
		{Id: 1, From: 4, To: 5},
		{Id: 1, From: 0, To: 6},
		{Id: 1, From: 1, To: 6},
		{Id: 1, From: 4, To: 6},
		{Id: 1, From: 5, To: 6},
		{Id: 1, From: 8, To: 9},
		{Id: 1, From: 8, To: 10},
		{Id: 1, From: 9, To: 10},
		{Id: 1, From: 8, To: 11},
		{Id: 1, From: 9, To: 11},
		{Id: 1, From: 10, To: 11},
		{Id: 1, From: 12, To: 15},
		{Id: 2, From: 9, To: 16},
		{Id: 1, From: 16, To: 17},
	})
}

func TestUCPGraphConvergentAlternationReportsEachRangeOnce(t *testing.T) {
	t.Parallel()
	db, err := scankit.Compile([]scankit.Expression{{
		Id:      1,
		Pattern: `(?:\p{L}|\p{L})\d\p{L}`,
		Flags:   scankit.CompileUTF8 | scankit.CompileUnicodeProperties,
	}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := db.Scan([]byte("A1BA1B"))
	if err != nil {
		t.Fatal(err)
	}
	assertMatchesEqual(t, got, []scankit.Match{{Id: 1, From: 0, To: 3}, {Id: 1, From: 3, To: 6}})
}

func TestUCPPropertyGroupRepeatSupportsCaselessAndAllowEmpty(t *testing.T) {
	t.Parallel()
	db, err := scankit.Compile([]scankit.Expression{
		{Id: 1, Pattern: `(?:Ä|x)+`, Flags: scankit.CompileUTF8 | scankit.CompileUnicodeProperties | scankit.CompileCaseless},
		{Id: 2, Pattern: `(?:\p{L}|\d)*`, Flags: scankit.CompileUTF8 | scankit.CompileUnicodeProperties | scankit.CompileAllowEmpty},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := db.Scan([]byte("äX!"))
	if err != nil {
		t.Fatal(err)
	}
	assertMatchesEqual(t, got, []scankit.Match{
		{Id: 2, From: 0, To: 0},
		{Id: 1, From: 0, To: 2},
		{Id: 2, From: 0, To: 2},
		{Id: 2, From: 2, To: 2},
		{Id: 1, From: 0, To: 3},
		{Id: 1, From: 2, To: 3},
		{Id: 2, From: 0, To: 3},
		{Id: 2, From: 2, To: 3},
		{Id: 2, From: 3, To: 3},
		{Id: 2, From: 4, To: 4},
	})
}

func TestUCPPropertyGroupGraphBoundsCompilerResources(t *testing.T) {
	t.Parallel()
	_, err := scankit.Compile([]scankit.Expression{{
		Id:      1,
		Pattern: `(?:\p{L}){513}`,
		Flags:   scankit.CompileUTF8 | scankit.CompileUnicodeProperties,
	}})
	if !errors.Is(err, scankit.ErrRegexTooComplex) {
		t.Fatalf("Compile() error = %v, want ErrRegexTooComplex", err)
	}
}

func TestUCPGraphSupportsInternalZeroWidthAssertions(t *testing.T) {
	t.Parallel()
	db, err := scankit.Compile([]scankit.Expression{
		{Id: 1, Pattern: `\p{L}\B\d`, Flags: scankit.CompileUTF8 | scankit.CompileUnicodeProperties},
		{Id: 2, Pattern: `\p{L}\b!`, Flags: scankit.CompileUTF8 | scankit.CompileUnicodeProperties},
		{Id: 3, Pattern: `(?:^|!)\p{L}`, Flags: scankit.CompileUTF8 | scankit.CompileUnicodeProperties},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := db.Scan([]byte("A1!B!张3"))
	if err != nil {
		t.Fatal(err)
	}
	assertMatchesEqual(t, got, []scankit.Match{
		{Id: 3, From: 0, To: 1},
		{Id: 1, From: 0, To: 2},
		{Id: 3, From: 2, To: 4},
		{Id: 2, From: 3, To: 5},
		{Id: 3, From: 4, To: 8},
		{Id: 1, From: 5, To: 9},
	})
}

func TestUCPGraphZeroWidthAssertionsControlEmptyMatches(t *testing.T) {
	t.Parallel()
	if _, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: `(?:\A)`, Flags: scankit.CompileUTF8 | scankit.CompileUnicodeProperties}}); !errors.Is(err, scankit.ErrUnsupportedExpression) {
		t.Fatalf("Compile() error = %v, want ErrUnsupportedExpression", err)
	}
	db, err := scankit.Compile([]scankit.Expression{
		{Id: 1, Pattern: `(?:\A)`, Flags: scankit.CompileUTF8 | scankit.CompileUnicodeProperties | scankit.CompileAllowEmpty},
		{Id: 2, Pattern: `(?:\B)`, Flags: scankit.CompileUTF8 | scankit.CompileUnicodeProperties | scankit.CompileAllowEmpty},
		{Id: 3, Pattern: `\b`, Flags: scankit.CompileUTF8 | scankit.CompileUnicodeProperties | scankit.CompileAllowEmpty},
		{Id: 4, Pattern: `\z`, Flags: scankit.CompileUTF8 | scankit.CompileUnicodeProperties | scankit.CompileAllowEmpty},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := db.Scan([]byte("A!"))
	if err != nil {
		t.Fatal(err)
	}
	assertMatchesEqual(t, got, []scankit.Match{
		{Id: 1, From: 0, To: 0},
		{Id: 3, From: 0, To: 0},
		{Id: 3, From: 1, To: 1},
		{Id: 2, From: 2, To: 2},
		{Id: 4, From: 2, To: 2},
	})
}

func TestUCPGraphSupportsEndAssertionsAndCaselessProperties(t *testing.T) {
	t.Parallel()
	db, err := scankit.Compile([]scankit.Expression{
		{Id: 1, Pattern: `\p{L}(?:$|\z)`, Flags: scankit.CompileUTF8 | scankit.CompileUnicodeProperties},
		{Id: 2, Pattern: `(?:\p{Lu})+`, Flags: scankit.CompileUTF8 | scankit.CompileUnicodeProperties | scankit.CompileCaseless},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := db.Scan([]byte("aB"))
	if err != nil {
		t.Fatal(err)
	}
	assertMatchesEqual(t, got, []scankit.Match{
		{Id: 2, From: 0, To: 1},
		{Id: 1, From: 1, To: 2},
		{Id: 2, From: 0, To: 2},
		{Id: 2, From: 1, To: 2},
	})
}

func TestUCPOptionalAndStarRequireAllowEmpty(t *testing.T) {
	t.Parallel()
	if _, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: `\p{L}?`, Flags: scankit.CompileUTF8 | scankit.CompileUnicodeProperties}}); !errors.Is(err, scankit.ErrUnsupportedExpression) {
		t.Fatalf("Compile() error = %v, want ErrUnsupportedExpression", err)
	}
	db, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: `\p{L}*`, Flags: scankit.CompileUTF8 | scankit.CompileUnicodeProperties | scankit.CompileAllowEmpty}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := db.Scan([]byte("A"))
	if err != nil {
		t.Fatal(err)
	}
	assertMatchesEqual(t, got, []scankit.Match{{Id: 1, From: 0, To: 0}, {Id: 1, From: 0, To: 1}, {Id: 1, From: 1, To: 1}})
}

func TestUCPAbsoluteAnchors(t *testing.T) {
	t.Parallel()
	db, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: `^\p{L}+$`, Flags: scankit.CompileUTF8 | scankit.CompileUnicodeProperties}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := db.Scan([]byte("张A"))
	if err != nil {
		t.Fatal(err)
	}
	assertMatchesEqual(t, got, []scankit.Match{{Id: 1, From: 0, To: 4}})
	got, err = db.Scan([]byte("x张1"))
	if err != nil {
		t.Fatal(err)
	}
	assertMatchesEqual(t, got, nil)
}

func TestUCPTerminalUnicodeWordBoundaries(t *testing.T) {
	t.Parallel()
	db, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: `\b\p{L}+\b`, Flags: scankit.CompileUTF8 | scankit.CompileUnicodeProperties}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := db.Scan([]byte("!张A?"))
	if err != nil {
		t.Fatal(err)
	}
	assertMatchesEqual(t, got, []scankit.Match{{Id: 1, From: 1, To: 5}})
}

func TestUCPCaselessUnicodeLiterals(t *testing.T) {
	t.Parallel()
	db, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: `Ä+`, Flags: scankit.CompileUTF8 | scankit.CompileUnicodeProperties | scankit.CompileCaseless}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := db.Scan([]byte("äÄ"))
	if err != nil {
		t.Fatal(err)
	}
	assertMatchesEqual(t, got, []scankit.Match{{Id: 1, From: 0, To: 2}, {Id: 1, From: 0, To: 4}, {Id: 1, From: 2, To: 4}})
}

func TestUCPCaselessRanges(t *testing.T) {
	t.Parallel()
	db, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: `[a-z]+`, Flags: scankit.CompileUTF8 | scankit.CompileUnicodeProperties | scankit.CompileCaseless}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := db.Scan([]byte("AZ"))
	if err != nil {
		t.Fatal(err)
	}
	assertMatchesEqual(t, got, []scankit.Match{{Id: 1, From: 0, To: 1}, {Id: 1, From: 0, To: 2}, {Id: 1, From: 1, To: 2}})
}

func TestUCPCaselessUsesCompleteUnicodeSimpleFoldCycles(t *testing.T) {
	t.Parallel()
	db, err := scankit.Compile([]scankit.Expression{
		{Id: 1, Pattern: `Σ+`, Flags: scankit.CompileUTF8 | scankit.CompileUnicodeProperties | scankit.CompileCaseless},
		{Id: 2, Pattern: `[K-K]+`, Flags: scankit.CompileUTF8 | scankit.CompileUnicodeProperties | scankit.CompileCaseless},
		{Id: 3, Pattern: `\p{Lu}+`, Flags: scankit.CompileUTF8 | scankit.CompileUnicodeProperties | scankit.CompileCaseless},
		{Id: 4, Pattern: `[^\p{Lu}]+`, Flags: scankit.CompileUTF8 | scankit.CompileUnicodeProperties | scankit.CompileCaseless},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := db.Scan([]byte("σςΣ kK aB!"))
	if err != nil {
		t.Fatal(err)
	}
	assertMatchesEqual(t, got, []scankit.Match{
		{Id: 1, From: 0, To: 2},
		{Id: 3, From: 0, To: 2},
		{Id: 1, From: 0, To: 4},
		{Id: 1, From: 2, To: 4},
		{Id: 3, From: 0, To: 4},
		{Id: 3, From: 2, To: 4},
		{Id: 1, From: 0, To: 6},
		{Id: 1, From: 2, To: 6},
		{Id: 1, From: 4, To: 6},
		{Id: 3, From: 0, To: 6},
		{Id: 3, From: 2, To: 6},
		{Id: 3, From: 4, To: 6},
		{Id: 4, From: 6, To: 7},
		{Id: 2, From: 7, To: 8},
		{Id: 3, From: 7, To: 8},
		{Id: 2, From: 7, To: 11},
		{Id: 2, From: 8, To: 11},
		{Id: 3, From: 7, To: 11},
		{Id: 3, From: 8, To: 11},
		{Id: 4, From: 11, To: 12},
		{Id: 3, From: 12, To: 13},
		{Id: 3, From: 12, To: 14},
		{Id: 3, From: 13, To: 14},
		{Id: 4, From: 14, To: 15},
	})
}

func TestUCPRulesMixWithASCIILiteralsInOneDatabase(t *testing.T) {
	t.Parallel()
	db, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: `\p{Han}+`, Flags: scankit.CompileUTF8 | scankit.CompileUnicodeProperties}, {Id: 2, Pattern: "token"}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := db.Scan([]byte("张token"))
	if err != nil {
		t.Fatal(err)
	}
	assertMatchesEqual(t, got, []scankit.Match{{Id: 1, From: 0, To: 3}, {Id: 2, From: 3, To: 8}})
}

func TestUCPRulesMixWithSimpleByteRepeatInOneScan(t *testing.T) {
	t.Parallel()
	db, err := scankit.Compile([]scankit.Expression{
		{Id: 1, Pattern: `\p{Han}+`, Flags: scankit.CompileUTF8 | scankit.CompileUnicodeProperties},
		{Id: 2, Pattern: `\d+`},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := db.Scan([]byte("张12A3"))
	if err != nil {
		t.Fatal(err)
	}
	assertMatchesEqual(t, got, []scankit.Match{
		{Id: 1, From: 0, To: 3},
		{Id: 2, From: 3, To: 4},
		{Id: 2, From: 3, To: 5},
		{Id: 2, From: 4, To: 5},
		{Id: 2, From: 6, To: 7},
	})
}

func TestUCPRulesMixWithUnicodeLiteralsInOneDatabase(t *testing.T) {
	t.Parallel()
	db, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: `\p{Han}+`, Flags: scankit.CompileUTF8 | scankit.CompileUnicodeProperties}, {Id: 2, Pattern: "姓名", Flags: scankit.CompileUTF8 | scankit.CompileUnicodeProperties}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := db.Scan([]byte("姓名张"))
	if err != nil {
		t.Fatal(err)
	}
	assertMatchesEqual(t, got, []scankit.Match{{Id: 1, From: 0, To: 3}, {Id: 1, From: 0, To: 6}, {Id: 1, From: 3, To: 6}, {Id: 2, From: 0, To: 6}, {Id: 1, From: 0, To: 9}, {Id: 1, From: 3, To: 9}, {Id: 1, From: 6, To: 9}})
}

func TestUCPRulesMixWithByteRegexApproximateAndCombinationInOneScan(t *testing.T) {
	t.Parallel()
	db, err := scankit.Compile([]scankit.Expression{
		{Id: 1, Pattern: `\p{Han}+`, Flags: scankit.CompileUTF8 | scankit.CompileUnicodeProperties},
		{Id: 2, Pattern: `\d+`},
		{Id: 3, Pattern: "token", Ext: &scankit.ExpressionExt{Flags: scankit.ExtHammingDistance, HammingDistance: 1}},
		{Id: 4, Pattern: "1&2", Flags: scankit.CompileCombination},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := db.Scan([]byte("张12 token"))
	if err != nil {
		t.Fatal(err)
	}
	assertMatchesEqual(t, got, []scankit.Match{
		{Id: 1, From: 0, To: 3},
		{Id: 2, From: 3, To: 4},
		{Id: 4, From: 0, To: 4},
		{Id: 2, From: 3, To: 5},
		{Id: 2, From: 4, To: 5},
		{Id: 3, From: 6, To: 11},
	})
}

func TestUCPApproximateFixedRuneSequences(t *testing.T) {
	t.Parallel()
	db, err := scankit.Compile([]scankit.Expression{
		{
			Id:      1,
			Pattern: `ID:\d{2}`,
			Flags:   scankit.CompileUTF8 | scankit.CompileUnicodeProperties,
			Ext:     &scankit.ExpressionExt{Flags: scankit.ExtHammingDistance, HammingDistance: 1},
		},
		{
			Id:      2,
			Pattern: `\p{Han}\d`,
			Flags:   scankit.CompileUTF8 | scankit.CompileUnicodeProperties,
			Ext:     &scankit.ExpressionExt{Flags: scankit.ExtEditDistance, EditDistance: 1},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := db.Scan([]byte("ID:1A 张A"))
	if err != nil {
		t.Fatal(err)
	}
	assertMatchesEqual(t, got, []scankit.Match{
		{Id: 2, From: 3, To: 4},
		{Id: 2, From: 2, To: 4},
		{Id: 1, From: 0, To: 5},
		{Id: 2, From: 6, To: 9},
		{Id: 2, From: 6, To: 10},
	})
	if _, err := scankit.Compile([]scankit.Expression{{
		Id:      3,
		Pattern: `(?:\p{Han}|\d)+`,
		Flags:   scankit.CompileUTF8 | scankit.CompileUnicodeProperties,
		Ext:     &scankit.ExpressionExt{Flags: scankit.ExtEditDistance, EditDistance: 1},
	}}); !errors.Is(err, scankit.ErrUnsupportedExtension) {
		t.Fatalf("Compile() error = %v, want ErrUnsupportedExtension", err)
	}
	asciiDB, err := scankit.Compile([]scankit.Expression{{
		Id:      4,
		Pattern: "token",
		Flags:   scankit.CompileUTF8 | scankit.CompileUnicodeProperties,
		Ext:     &scankit.ExpressionExt{Flags: scankit.ExtHammingDistance, HammingDistance: 1},
	}})
	if err != nil {
		t.Fatal(err)
	}
	got, err = asciiDB.Scan([]byte("tokem"))
	if err != nil {
		t.Fatal(err)
	}
	assertMatchesEqual(t, got, []scankit.Match{{Id: 4, From: 0, To: 5}})
}

func TestUCPPropertyRulesRejectUnsupportedFormsAndMixing(t *testing.T) {
	t.Parallel()
	for _, pattern := range []string{`\p{unknown}+`, `[]`, `[[`} {
		_, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: pattern, Flags: scankit.CompileUTF8 | scankit.CompileUnicodeProperties}})
		if !errors.Is(err, scankit.ErrUnsupportedExpression) {
			t.Fatalf("Compile(%q) error = %v, want ErrUnsupportedExpression", pattern, err)
		}
	}
	db, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: `\p{L}+`, Flags: scankit.CompileUTF8 | scankit.CompileUnicodeProperties}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Scan([]byte{'A', 0xff}); !errors.Is(err, scankit.ErrInvalidUTF8) {
		t.Fatalf("Scan() error = %v, want ErrInvalidUTF8", err)
	}
}

func TestEquivalentUnanchoredRulesShareExecutionWithoutChangingEvents(t *testing.T) {
	t.Parallel()
	data := []byte("a12b3")
	shared, err := scankit.Compile([]scankit.Expression{
		{Id: 1, Pattern: `\d+`},
		{Id: 2, Pattern: `[0-9]+`, Ext: &scankit.ExpressionExt{Flags: scankit.ExtMinOffset, MinOffset: 2}},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := shared.Scan(data)
	if err != nil {
		t.Fatal(err)
	}
	assertMatchesEqual(t, got, []scankit.Match{
		{Id: 1, From: 1, To: 2},
		{Id: 2, From: 1, To: 2},
		{Id: 1, From: 1, To: 3},
		{Id: 1, From: 2, To: 3},
		{Id: 2, From: 1, To: 3},
		{Id: 2, From: 2, To: 3},
		{Id: 1, From: 4, To: 5},
		{Id: 2, From: 4, To: 5},
	})
}

func TestEquivalentAnchoredRulesShareVerificationWithoutChangingEvents(t *testing.T) {
	t.Parallel()
	db, err := scankit.Compile([]scankit.Expression{
		{Id: 1, Pattern: `ID:\d{2}`},
		{Id: 2, Pattern: `ID:[0-9]{2}`, Ext: &scankit.ExpressionExt{Flags: scankit.ExtMaxOffset, MaxOffset: 5}},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := db.Scan([]byte("ID:12 ID:34"))
	if err != nil {
		t.Fatal(err)
	}
	assertMatchesEqual(t, got, []scankit.Match{
		{Id: 1, From: 0, To: 5},
		{Id: 2, From: 0, To: 5},
		{Id: 1, From: 6, To: 11},
	})
}

func TestQuietRuleWithoutCombinationConsumerHasNoScanEffect(t *testing.T) {
	t.Parallel()
	data := []byte("a12 token 34 token")
	control, err := scankit.Compile([]scankit.Expression{{Id: 2, Pattern: "token"}})
	if err != nil {
		t.Fatal(err)
	}
	optimized, err := scankit.Compile([]scankit.Expression{
		{Id: 1, Pattern: `\d+`, Flags: scankit.CompileQuiet},
		{Id: 2, Pattern: "token"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want, err := control.Scan(data)
	if err != nil {
		t.Fatal(err)
	}
	got, err := optimized.Scan(data)
	if err != nil {
		t.Fatal(err)
	}
	assertMatchesEqual(t, got, want)
}

func TestQuietRuleReferencedByCombinationStillProducesOperandEvent(t *testing.T) {
	t.Parallel()
	db, err := scankit.Compile([]scankit.Expression{
		{Id: 1, Pattern: `\d+`, Flags: scankit.CompileQuiet},
		{Id: 2, Pattern: "token", Flags: scankit.CompileQuiet},
		{Id: 3, Pattern: "1&2", Flags: scankit.CompileCombination},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := db.Scan([]byte("x12 token"))
	if err != nil {
		t.Fatal(err)
	}
	assertMatchesEqual(t, got, []scankit.Match{{Id: 3, From: 0, To: 9}})
}

func TestNullableExpressionRequiresAllowEmpty(t *testing.T) {
	t.Parallel()
	_, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: `a*`}})
	if !errors.Is(err, scankit.ErrUnsupportedExpression) {
		t.Fatalf("Compile() error = %v, want ErrUnsupportedExpression", err)
	}
}

func FuzzAdvancedFlagsProduceValidBlockRanges(f *testing.F) {
	f.Add([]byte(""))
	f.Add([]byte("aaa token a12b3"))
	f.Add([]byte{0xff, 'a', '1'})

	db, err := scankit.Compile([]scankit.Expression{
		{Id: 1, Pattern: `a*`, Flags: scankit.CompileAllowEmpty},
		{Id: 2, Pattern: `\d+`, Flags: scankit.CompileLeftmostStart | scankit.CompilePrefilter},
		{Id: 3, Pattern: "token", Ext: &scankit.ExpressionExt{Flags: scankit.ExtHammingDistance, HammingDistance: 1}},
		{Id: 4, Pattern: "ab", Ext: &scankit.ExpressionExt{Flags: scankit.ExtEditDistance, EditDistance: 1}},
		{Id: 5, Pattern: "张三", Flags: scankit.CompileUTF8},
	})
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1024 {
			t.Skip()
		}
		matches, err := db.Scan(data)
		if !utf8.Valid(data) {
			if !errors.Is(err, scankit.ErrInvalidUTF8) {
				t.Fatalf("Scan() error = %v, want ErrInvalidUTF8", err)
			}
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		for _, match := range matches {
			if match.From > match.To || match.To > uint64(len(data)) {
				t.Fatalf("invalid match range %#v for %d-byte input", match, len(data))
			}
		}
	})
}

func FuzzUCPPropertyRulesReportValidRuneRanges(f *testing.F) {
	f.Add([]byte("张三A１２3"))
	f.Add([]byte(""))
	f.Add([]byte{'A', 0xff})

	db, err := scankit.Compile([]scankit.Expression{{
		Id:      1,
		Pattern: `\p{L}+`,
		Flags:   scankit.CompileUTF8 | scankit.CompileUnicodeProperties,
	}, {
		Id:      2,
		Pattern: `\p{L}\p{Nd}`,
		Flags:   scankit.CompileUTF8 | scankit.CompileUnicodeProperties,
	}})
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 4_096 {
			t.Skip()
		}
		matches, err := db.Scan(data)
		if !utf8.Valid(data) {
			if !errors.Is(err, scankit.ErrInvalidUTF8) {
				t.Fatalf("Scan() error = %v, want ErrInvalidUTF8", err)
			}
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		for _, match := range matches {
			from, to := int(match.From), int(match.To)
			if from >= to || to > len(data) || !utf8.RuneStart(data[from]) || to < len(data) && !utf8.RuneStart(data[to]) {
				t.Fatalf("invalid rune range %#v for %q", match, data)
			}
			switch match.Id {
			case 1:
				for _, value := range string(data[from:to]) {
					if !unicode.IsLetter(value) {
						t.Fatalf("match %#v includes non-letter %U", match, value)
					}
				}
			case 2:
				values := []rune(string(data[from:to]))
				if len(values) != 2 || !unicode.IsLetter(values[0]) || !unicode.Is(unicode.Nd, values[1]) {
					t.Fatalf("match %#v is not letter-plus-decimal-number", match)
				}
			default:
				t.Fatalf("unexpected expression ID %d", match.Id)
			}
		}
	})
}

func FuzzUCPLineBreakScanIntoMatchesScan(f *testing.F) {
	f.Add([]byte("a\r\nb\rc\nd\u0085e\u2028f\u2029"))
	f.Add([]byte("\r"))
	f.Add([]byte("\r\n"))
	f.Add([]byte{0xff, '\r', '\n'})
	database, err := scankit.Compile([]scankit.Expression{{
		Id:      1,
		Pattern: `\R`,
		Flags:   scankit.CompileUTF8 | scankit.CompileUnicodeProperties,
	}})
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		want, err := database.Scan(data)
		if err != nil {
			if !errors.Is(err, scankit.ErrInvalidUTF8) {
				t.Fatal(err)
			}
			return
		}
		got, err := database.ScanInto(data, make([]scankit.Match, 0, len(want)))
		if err != nil {
			t.Fatal(err)
		}
		assertMatchesEqual(t, got, want)
		for _, match := range got {
			if match.From >= match.To || match.To > uint64(len(data)) {
				t.Fatalf("invalid line-break range %#v for %q", match, data)
			}
		}
	})
}

func FuzzUCPShorthandsAndClassesReportValidRuneRanges(f *testing.F) {
	f.Add([]byte("A１２　张_9"))
	f.Add([]byte("ID:１２"))
	f.Add([]byte(""))
	f.Add([]byte{'A', 0xff})

	db, err := scankit.Compile([]scankit.Expression{
		{Id: 1, Pattern: `\d{2}`, Flags: scankit.CompileUTF8 | scankit.CompileUnicodeProperties},
		{Id: 2, Pattern: `\s`, Flags: scankit.CompileUTF8 | scankit.CompileUnicodeProperties},
		{Id: 3, Pattern: `\w{2}`, Flags: scankit.CompileUTF8 | scankit.CompileUnicodeProperties},
		{Id: 4, Pattern: `[\p{L}\d]{2}`, Flags: scankit.CompileUTF8 | scankit.CompileUnicodeProperties},
		{Id: 5, Pattern: `\p{L}{1,2}\d`, Flags: scankit.CompileUTF8 | scankit.CompileUnicodeProperties},
		{Id: 6, Pattern: `ID:\d{2}`, Flags: scankit.CompileUTF8 | scankit.CompileUnicodeProperties},
	})
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1_024 {
			t.Skip()
		}
		matches, err := db.Scan(data)
		if !utf8.Valid(data) {
			if !errors.Is(err, scankit.ErrInvalidUTF8) {
				t.Fatalf("Scan() error = %v, want ErrInvalidUTF8", err)
			}
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		for _, match := range matches {
			from, to := int(match.From), int(match.To)
			if from >= to || to > len(data) || !utf8.RuneStart(data[from]) || to < len(data) && !utf8.RuneStart(data[to]) {
				t.Fatalf("invalid rune range %#v for %q", match, data)
			}
			values := []rune(string(data[from:to]))
			switch match.Id {
			case 1:
				if len(values) != 2 || !unicode.Is(unicode.Categories["Nd"], values[0]) || !unicode.Is(unicode.Categories["Nd"], values[1]) {
					t.Fatalf("match %#v is not two decimal numbers", match)
				}
			case 2:
				if len(values) != 1 || !unicode.IsSpace(values[0]) {
					t.Fatalf("match %#v is not unicode whitespace", match)
				}
			case 3:
				if len(values) != 2 || !isUCPWordRune(values[0]) || !isUCPWordRune(values[1]) {
					t.Fatalf("match %#v is not two UCP word runes", match)
				}
			case 4:
				if len(values) != 2 || !isUnicodeLetterOrDecimal(values[0]) || !isUnicodeLetterOrDecimal(values[1]) {
					t.Fatalf("match %#v is not two class members", match)
				}
			case 5:
				if (len(values) != 2 && len(values) != 3) || !unicode.Is(unicode.Categories["Nd"], values[len(values)-1]) {
					t.Fatalf("match %#v has invalid repeated-property shape", match)
				}
				for _, value := range values[:len(values)-1] {
					if !unicode.IsLetter(value) {
						t.Fatalf("match %#v includes non-letter prefix %U", match, value)
					}
				}
			case 6:
				if len(values) != 5 || values[0] != 'I' || values[1] != 'D' || values[2] != ':' || !unicode.Is(unicode.Categories["Nd"], values[3]) || !unicode.Is(unicode.Categories["Nd"], values[4]) {
					t.Fatalf("match %#v does not have the literal prefix", match)
				}
			default:
				t.Fatalf("unexpected expression ID %d", match.Id)
			}
		}
	})
}

func FuzzUCPGroupedRepeatsReportValidRuneRanges(f *testing.F) {
	f.Add([]byte("A１B2 ID:张:"))
	f.Add([]byte(""))
	f.Add([]byte{'A', 0xff})

	db, err := scankit.Compile([]scankit.Expression{{
		Id:      1,
		Pattern: `(?:(?:\p{L}|\d){1,2}:)+`,
		Flags:   scankit.CompileUTF8 | scankit.CompileUnicodeProperties,
	}})
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 4_096 {
			t.Skip()
		}
		matches, err := db.Scan(data)
		if !utf8.Valid(data) {
			if !errors.Is(err, scankit.ErrInvalidUTF8) {
				t.Fatalf("Scan() error = %v, want ErrInvalidUTF8", err)
			}
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		for _, match := range matches {
			from, to := int(match.From), int(match.To)
			if from >= to || to > len(data) || !utf8.RuneStart(data[from]) || to < len(data) && !utf8.RuneStart(data[to]) {
				t.Fatalf("invalid grouped-rune range %#v for %q", match, data)
			}
		}
	})
}

func FuzzUCPZeroWidthAndCaselessReportValidRuneRanges(f *testing.F) {
	f.Add([]byte("σςΣ A1!张3"))
	f.Add([]byte(""))
	f.Add([]byte{'A', 0xff})

	db, err := scankit.Compile([]scankit.Expression{
		{Id: 1, Pattern: `(?:\A|\B)`, Flags: scankit.CompileUTF8 | scankit.CompileUnicodeProperties | scankit.CompileAllowEmpty},
		{Id: 2, Pattern: `(?:Σ|\p{Lu})+`, Flags: scankit.CompileUTF8 | scankit.CompileUnicodeProperties | scankit.CompileCaseless},
	})
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 4_096 {
			t.Skip()
		}
		matches, err := db.Scan(data)
		if !utf8.Valid(data) {
			if !errors.Is(err, scankit.ErrInvalidUTF8) {
				t.Fatalf("Scan() error = %v, want ErrInvalidUTF8", err)
			}
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		for _, match := range matches {
			from, to := int(match.From), int(match.To)
			if from > to || to > len(data) || from < len(data) && !utf8.RuneStart(data[from]) || to < len(data) && !utf8.RuneStart(data[to]) {
				t.Fatalf("invalid zero-width/caseless rune range %#v for %q", match, data)
			}
			if match.Id == 1 && from != to {
				t.Fatalf("assertion-only rule returned non-empty match %#v", match)
			}
		}
	})
}

func FuzzUCPApproximateRulesReportValidRuneRanges(f *testing.F) {
	f.Add([]byte("张1 ID:12"))
	f.Add([]byte(""))
	f.Add([]byte{'A', 0xff})

	db, err := scankit.Compile([]scankit.Expression{
		{Id: 1, Pattern: `\p{Han}\d`, Flags: scankit.CompileUTF8 | scankit.CompileUnicodeProperties, Ext: &scankit.ExpressionExt{Flags: scankit.ExtHammingDistance, HammingDistance: 1}},
		{Id: 2, Pattern: `ID:\d{2}`, Flags: scankit.CompileUTF8 | scankit.CompileUnicodeProperties, Ext: &scankit.ExpressionExt{Flags: scankit.ExtEditDistance, EditDistance: 1}},
	})
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 2_048 {
			t.Skip()
		}
		matches, err := db.Scan(data)
		if !utf8.Valid(data) {
			if !errors.Is(err, scankit.ErrInvalidUTF8) {
				t.Fatalf("Scan() error = %v, want ErrInvalidUTF8", err)
			}
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		for _, match := range matches {
			from, to := int(match.From), int(match.To)
			if from >= to || to > len(data) || !utf8.RuneStart(data[from]) || to < len(data) && !utf8.RuneStart(data[to]) {
				t.Fatalf("invalid approximate-rune range %#v for %q", match, data)
			}
		}
	})
}

func FuzzMixedUCPAndByteRulesReportValidRanges(f *testing.F) {
	f.Add([]byte("张12 token"))
	f.Add([]byte(""))
	f.Add([]byte{'A', 0xff})

	db, err := scankit.Compile([]scankit.Expression{
		{Id: 1, Pattern: `\p{Han}+`, Flags: scankit.CompileUTF8 | scankit.CompileUnicodeProperties},
		{Id: 2, Pattern: `\d+`},
		{Id: 3, Pattern: "1&2", Flags: scankit.CompileCombination},
	})
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 4_096 {
			t.Skip()
		}
		matches, err := db.Scan(data)
		if !utf8.Valid(data) {
			if !errors.Is(err, scankit.ErrInvalidUTF8) {
				t.Fatalf("Scan() error = %v, want ErrInvalidUTF8", err)
			}
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		for _, match := range matches {
			if match.From > match.To || match.To > uint64(len(data)) {
				t.Fatalf("invalid mixed-rule range %#v for %q", match, data)
			}
		}
	})
}

func isUCPWordRune(value rune) bool {
	return unicode.IsLetter(value) || unicode.IsMark(value) || unicode.IsNumber(value) || unicode.Is(unicode.Categories["Pc"], value)
}

func isUCPHorizontalSpace(value rune) bool {
	switch value {
	case '\t', ' ', '\u00a0', '\u1680', '\u180e', '\u2000', '\u2001', '\u2002', '\u2003', '\u2004', '\u2005', '\u2006', '\u2007', '\u2008', '\u2009', '\u200a', '\u202f', '\u205f', '\u3000':
		return true
	}
	return false
}

func unicodeSimpleFoldContainsUpper(value rune) bool {
	if unicode.IsUpper(value) {
		return true
	}
	for folded := unicode.SimpleFold(value); folded != value; folded = unicode.SimpleFold(folded) {
		if unicode.IsUpper(folded) {
			return true
		}
	}
	return false
}

func isUnicodeLetterOrDecimal(value rune) bool {
	return unicode.IsLetter(value) || unicode.Is(unicode.Categories["Nd"], value)
}

func FuzzSharedUnanchoredRulesMatchSingleRuleExecution(f *testing.F) {
	f.Add([]byte("a12b3"))
	f.Add([]byte(""))
	f.Add([]byte{0xff, '1', '2'})
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1024 {
			t.Skip()
		}
		shared, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: `\d+`}, {Id: 2, Pattern: `[0-9]+`}})
		if err != nil {
			t.Fatal(err)
		}
		individual, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: `\d+`}})
		if err != nil {
			t.Fatal(err)
		}
		sharedMatches, err := shared.Scan(data)
		if err != nil {
			t.Fatal(err)
		}
		individualMatches, err := individual.Scan(data)
		if err != nil {
			t.Fatal(err)
		}
		for _, id := range []uint32{1, 2} {
			got := make([]scankit.Match, 0, len(individualMatches))
			for _, match := range sharedMatches {
				if match.Id == id {
					match.Id = 1
					got = append(got, match)
				}
			}
			assertMatchesEqual(t, got, individualMatches)
		}
	})
}

func FuzzSharedAnchoredRulesMatchSingleRuleExecution(f *testing.F) {
	f.Add([]byte("ID:12 ID:34"))
	f.Add([]byte(""))
	f.Add([]byte{0xff, 'I', 'D', ':', '1', '2'})
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1024 {
			t.Skip()
		}
		shared, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: `ID:\d{2}`}, {Id: 2, Pattern: `ID:[0-9]{2}`}})
		if err != nil {
			t.Fatal(err)
		}
		individual, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: `ID:\d{2}`}})
		if err != nil {
			t.Fatal(err)
		}
		sharedMatches, err := shared.Scan(data)
		if err != nil {
			t.Fatal(err)
		}
		individualMatches, err := individual.Scan(data)
		if err != nil {
			t.Fatal(err)
		}
		for _, id := range []uint32{1, 2} {
			got := make([]scankit.Match, 0, len(individualMatches))
			for _, match := range sharedMatches {
				if match.Id == id {
					match.Id = 1
					got = append(got, match)
				}
			}
			assertMatchesEqual(t, got, individualMatches)
		}
	})
}

func FuzzQuietUnobservedRuleDoesNotChangeVisibleMatches(f *testing.F) {
	f.Add([]byte("12 token"))
	f.Add([]byte(""))
	f.Add([]byte{0xff, '1', '2', 't'})
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 1024 {
			t.Skip()
		}
		control, err := scankit.Compile([]scankit.Expression{{Id: 2, Pattern: "token"}})
		if err != nil {
			t.Fatal(err)
		}
		withQuiet, err := scankit.Compile([]scankit.Expression{
			{Id: 1, Pattern: `\d+`, Flags: scankit.CompileQuiet},
			{Id: 2, Pattern: "token"},
		})
		if err != nil {
			t.Fatal(err)
		}
		want, err := control.Scan(data)
		if err != nil {
			t.Fatal(err)
		}
		got, err := withQuiet.Scan(data)
		if err != nil {
			t.Fatal(err)
		}
		assertMatchesEqual(t, got, want)
	})
}
