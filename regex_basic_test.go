package scankit_test

import (
	"regexp"
	"testing"

	"github.com/smartwalle/scankit"
)

func TestVariableWidthClassRepeatExactMatchesGoRegexpAtEveryEnd(t *testing.T) {
	t.Parallel()
	data := []byte("prefix=ABABABAB suffix=ABABABA")
	database, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: `(?:A|B){6,8}`}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := database.Scan(data)
	if err != nil {
		t.Fatal(err)
	}
	want := goRegexpVariableClassRepeatAllEnds(data, regexp.MustCompile(`^(?:A|B){6,8}$`))
	assertMatchesEqual(t, got, want)
}

func goRegexpVariableClassRepeatAllEnds(data []byte, expression *regexp.Regexp) []scankit.Match {
	matches := make([]scankit.Match, 0, len(data))
	for end := 1; end <= len(data); end++ {
		for length := 8; length >= 6; length-- {
			start := end - length
			if start < 0 || !expression.Match(data[start:end]) {
				continue
			}
			matches = append(matches, scankit.Match{Id: 1, From: uint64(start), To: uint64(end)})
			break
		}
	}
	return matches
}
