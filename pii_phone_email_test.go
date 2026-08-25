package scankit_test

import (
	"regexp"
	"testing"

	"github.com/smartwalle/scankit"
)

const phonePattern = `1[3-9][0-9]{9}`

// emailTLDPattern accepts common single-level TLDs for the PII examples.
const emailTLDPattern = `(com|net|org|cn|io|dev|app|edu|gov|info|biz|me|xyz|online|site|tech|store|cloud|ai|pro|mobi|name|tv|cc|hk|jp|uk|de|fr|au|ca|us)`

// emailPattern intentionally bounds local-part and domain lengths. It
// supports common single-level TLDs such as .com, .cn, .net and .org; the
// mandatory "@" anchor remains usable by the compiled scanner.
const emailPattern = `[A-Za-z0-9._%+-]{1,64}@[A-Za-z0-9-]{1,63}\.` + emailTLDPattern

func TestPhonePatternMatchesGoRegexp(t *testing.T) {
	t.Parallel()

	data := []byte("valid=13800138000 invalid=12345678901 valid=19912345678")
	got := scanPhoneWithScankit(t, data)
	want := findAllOverlapping(regexp.MustCompile(phonePattern), data)
	assertRangesEqual(t, got, want)
}

func TestPhoneAndEmailPatternsMatchGoRegexp(t *testing.T) {
	t.Parallel()

	data := []byte("phone=13800138000 com=alice.smith42@example.com cn=bob@example.cn org=ops@example.org invalid=bad@domain.invalid phone=19912345678")
	database, err := scankit.Compile([]scankit.Expression{
		{Id: 1, Pattern: phonePattern},
		{Id: 2, Pattern: emailPattern},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := map[uint32][][2]int{}
	matches, err := database.Scan(data)
	if err != nil {
		t.Fatal(err)
	}
	for _, match := range matches {
		got[match.Id] = append(got[match.Id], [2]int{int(match.From), int(match.To)})
	}
	assertRangesEqual(t, got[1], findAllOverlapping(regexp.MustCompile(phonePattern), data))
	assertRangesEqual(t, got[2], regexp.MustCompile(emailPattern).FindAllIndex(data, -1))
}

func FuzzPhonePatternMatchesGoRegexp(f *testing.F) {
	f.Add([]byte("13800138000"))
	f.Add([]byte("invalid=12345678901"))
	f.Add([]byte("before 19912345678 after"))
	f.Add([]byte{0xff, '1', '3', '8', '0', '0', '1', '3', '8', '0', '0'})

	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 4_096 {
			t.Skip()
		}
		got := scanPhoneWithScankit(t, data)
		want := findAllOverlapping(regexp.MustCompile(phonePattern), data)
		assertRangesEqual(t, got, want)
	})
}

func FuzzPhoneAndEmailPatternsMatchGoRegexp(f *testing.F) {
	f.Add([]byte("phone=13800138000 email=alice.smith42@example.com"))
	f.Add([]byte("bob@domain.cn ops@domain.org bad@domain.invalid 19912345678"))
	f.Add([]byte{})

	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 4_096 {
			t.Skip()
		}
		database, err := scankit.Compile([]scankit.Expression{
			{Id: 1, Pattern: phonePattern},
			{Id: 2, Pattern: emailPattern},
		})
		if err != nil {
			t.Fatal(err)
		}
		got := map[uint32][][2]int{}
		matches, err := database.Scan(data)
		if err != nil {
			t.Fatal(err)
		}
		for _, match := range matches {
			got[match.Id] = append(got[match.Id], [2]int{int(match.From), int(match.To)})
		}
		assertRangesEqual(t, got[1], findAllOverlapping(regexp.MustCompile(phonePattern), data))
		assertRangesSubset(t, got[2], findAllOverlapping(regexp.MustCompile(emailPattern), data))
	})
}

func scanPhoneWithScankit(t testing.TB, data []byte) [][2]int {
	t.Helper()
	database, err := scankit.Compile([]scankit.Expression{{Id: 1, Pattern: phonePattern}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := database.Scan(data)
	if err != nil {
		t.Fatal(err)
	}
	matches := make([][2]int, len(got))
	for index, match := range got {
		matches[index] = [2]int{int(match.From), int(match.To)}
	}
	return matches
}

func findAllOverlapping(re *regexp.Regexp, data []byte) [][]int {
	var matches [][]int
	for offset := 0; offset < len(data); {
		match := re.FindIndex(data[offset:])
		if match == nil {
			break
		}
		match[0] += offset
		match[1] += offset
		matches = append(matches, match)
		offset = match[0] + 1
	}
	return matches
}

func assertRangesSubset(t testing.TB, got [][2]int, candidates [][]int) {
	t.Helper()
	allowed := make(map[[2]int]struct{}, len(candidates))
	for _, candidate := range candidates {
		allowed[[2]int{candidate[0], candidate[1]}] = struct{}{}
	}
	for _, match := range got {
		if _, ok := allowed[match]; !ok {
			t.Fatalf("match %v is not accepted by Go regexp", match)
		}
	}
}

func assertRangesEqual(t testing.TB, got [][2]int, want [][]int) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("match count = %d, want %d; got = %v; want = %v", len(got), len(want), got, want)
	}
	for index := range want {
		if got[index][0] != want[index][0] || got[index][1] != want[index][1] {
			t.Fatalf("match %d = %v, want %v", index, got[index], want[index])
		}
	}
}
