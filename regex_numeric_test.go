package scankit_test

import (
	"regexp"
	"testing"

	"github.com/smartwalle/scankit"
)

func TestNumericRegexWordBoundarySemantics(t *testing.T) {
	t.Parallel()

	data := []byte("中文 seq=2026 embedded=alice42 code=7 build=v2 token=_8_ ip=10.24.8.16 tail=9001.")
	database, err := scankit.Compile([]scankit.Expression{
		{Id: 1, Pattern: `\d+`},
		{Id: 2, Pattern: `\b\d+\b`},
	})
	if err != nil {
		t.Fatal(err)
	}
	matches, err := database.Scan(data)
	if err != nil {
		t.Fatal(err)
	}

	got := [2][]string{}
	for _, match := range matches {
		if match.Id < 1 || match.Id > 2 {
			t.Fatalf("unexpected expression ID %d", match.Id)
		}
		got[match.Id-1] = append(got[match.Id-1], string(data[match.From:match.To]))
	}
	assertStringSlicesEqual(t, got[1], []string{"2026", "7", "10", "24", "8", "16", "9001"})
	assertStringSlicesEqual(t, got[0], []string{
		"2", "20", "202", "2026",
		"4", "42",
		"7",
		"2",
		"8",
		"1", "10", "2", "24", "8", "1", "16",
		"9", "90", "900", "9001",
	})
}

func FuzzNumericRegexWordBoundaryMatchesGoRegexp(f *testing.F) {
	f.Add([]byte("seq=2026 embedded=alice42 code=7 _8_ ip=10.24.8.16"))
	f.Add([]byte(""))
	f.Add([]byte{0xff, ' ', '4', '2', ' ', '_', '7', '_'})

	database, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: `\b\d+\b`}})
	if err != nil {
		f.Fatal(err)
	}
	goRegexp := regexp.MustCompile(`\b\d+\b`)
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 4_096 {
			t.Skip()
		}
		matches, err := database.Scan(data)
		if err != nil {
			t.Fatal(err)
		}
		got := make([][2]int, len(matches))
		for index, match := range matches {
			if match.Id != 1 || match.From >= match.To || match.To > uint64(len(data)) {
				t.Fatalf("invalid match %#v for %d bytes", match, len(data))
			}
			got[index] = [2]int{int(match.From), int(match.To)}
		}
		assertRangesEqual(t, got, goRegexp.FindAllIndex(data, -1))
	})
}

func assertStringSlicesEqual(t testing.TB, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("value count = %d, want %d; got = %q", len(got), len(want), got)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("value %d = %q, want %q", index, got[index], want[index])
		}
	}
}
