package scankit

import (
	"bytes"
	"sync"
	"testing"
)

func TestNewCompilesExpressions(t *testing.T) {
	engine, err := New([]Expression{{Id: 1, Pattern: "token"}})
	if err != nil {
		t.Fatal(err)
	}
	matches, err := engine.Scan([]byte("before token after"))
	if err != nil {
		t.Fatal(err)
	}
	want := []Match{{Id: 1, From: 7, To: 12}}
	if !equalMatches(matches, want) {
		t.Fatalf("matches = %#v, want %#v", matches, want)
	}
}

func TestEngineReplaceAndMask(t *testing.T) {
	engine, err := New([]Expression{{Id: 1, Pattern: "token"}})
	if err != nil {
		t.Fatal(err)
	}

	replaced, err := engine.Replace([]byte("token"), func(buf *bytes.Buffer, _ Match, _ []byte) {
		buf.WriteString("***")
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(replaced) != "***" {
		t.Fatalf("Replace() = %q, want %q", replaced, "***")
	}

	data := []byte("token token")
	var seen []Match
	masked, err := engine.Mask(data, func(match Match, value []byte) {
		seen = append(seen, match)
		copy(value, "#####")
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(masked) != "##### #####" || !bytes.Equal(masked, data) {
		t.Fatalf("Mask() = %q, want %q", masked, "##### #####")
	}
	wantMatches := []Match{{Id: 1, From: 0, To: 5}, {Id: 1, From: 6, To: 11}}
	if !equalMatches(seen, wantMatches) {
		t.Fatalf("Mask() matches = %#v, want %#v", seen, wantMatches)
	}
}

func TestEngineMaskResolvesOverlappingMatches(t *testing.T) {
	engine, err := New([]Expression{{Id: 1, Pattern: "token"}, {Id: 2, Pattern: "ok"}})
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("token")
	var seen []Match
	masked, err := engine.Mask(data, func(match Match, value []byte) {
		seen = append(seen, match)
		for index := range value {
			value[index] = '#'
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if string(masked) != "#####" || !bytes.Equal(masked, data) {
		t.Fatalf("Mask() = %q, want %q", masked, "#####")
	}
	if want := []Match{{Id: 1, From: 0, To: 5}}; !equalMatches(seen, want) {
		t.Fatalf("Mask() matches = %#v, want %#v", seen, want)
	}
}

func TestMask(t *testing.T) {
	t.Run("modifies resolved matches", func(t *testing.T) {
		data := []byte("token")
		matches := []Match{{Id: 2, From: 1, To: 3}, {Id: 1, From: 0, To: 5}}
		var seen []Match
		masked := Mask(data, matches, func(match Match, value []byte) {
			seen = append(seen, match)
			for index := range value {
				value[index] = '#'
			}
		})
		if string(masked) != "#####" || !bytes.Equal(masked, data) {
			t.Fatalf("masked = %q, want %q", masked, "#####")
		}
		if want := []Match{{Id: 1, From: 0, To: 5}}; !equalMatches(seen, want) {
			t.Fatalf("seen = %#v, want %#v", seen, want)
		}
	})

}

func TestEngineMaskReturnsInputWhenScanFails(t *testing.T) {
	engine, err := New([]Expression{{Id: 1, Pattern: `x`, Flags: CompileUTF8}})
	if err != nil {
		t.Fatal(err)
	}
	data := []byte{0xff}
	called := false
	masked, maskErr := engine.Mask(data, func(_ Match, _ []byte) {
		called = true
	})
	if maskErr == nil {
		t.Fatal("Mask() error = nil, want invalid UTF-8 error")
	}
	if !bytes.Equal(masked, data) || called {
		t.Fatalf("Mask() = %v, called = %t, want unchanged input without callback", masked, called)
	}
}

func TestEngineReplaceAndMaskConcurrently(t *testing.T) {
	engine, err := New([]Expression{{Id: 1, Pattern: "token"}})
	if err != nil {
		t.Fatal(err)
	}

	const workers = 8
	const iterations = 100
	var group sync.WaitGroup
	group.Add(workers)
	for range workers {
		go func() {
			defer group.Done()
			for range iterations {
				replaced, err := engine.Replace([]byte("token"), func(buf *bytes.Buffer, _ Match, _ []byte) {
					buf.WriteString("***")
				})
				if err != nil || string(replaced) != "***" {
					t.Errorf("Replace() = %q, %v", replaced, err)
					return
				}

				data := []byte("token")
				masked, err := engine.Mask(data, func(_ Match, value []byte) {
					for index := range value {
						value[index] = '#'
					}
				})
				if err != nil || string(masked) != "#####" || !bytes.Equal(masked, data) {
					t.Errorf("Mask() = %q, %v", masked, err)
					return
				}
			}
		}()
	}
	group.Wait()
}

func FuzzEngineMaskMutatesOnlyResolvedMatches(f *testing.F) {
	engine, err := New([]Expression{{Id: 1, Pattern: "token"}, {Id: 2, Pattern: "ok"}, {Id: 3, Pattern: `[0-9]{4}`}})
	if err != nil {
		f.Fatal(err)
	}
	f.Add([]byte("token 2026"))
	f.Add([]byte("ok token 0000"))
	f.Add([]byte{})
	f.Add([]byte{0xff, 't', 'o', 'k', 'e', 'n'})

	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 4_096 {
			t.Skip()
		}
		matches, err := engine.Scan(data)
		if err != nil {
			t.Fatal(err)
		}
		want := append([]byte(nil), data...)
		for _, match := range resolveOverlappingMatches(matches) {
			for index := match.From; index < match.To; index++ {
				want[index] = '#'
			}
		}
		if _, err := engine.Mask(data, func(_ Match, value []byte) {
			for index := range value {
				value[index] = '#'
			}
		}); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(data, want) {
			t.Fatalf("Mask() data = %q, want %q", data, want)
		}
	})
}

func FuzzMaskMutatesOnlyResolvedMatches(f *testing.F) {
	f.Add([]byte("token"))
	f.Add([]byte("token 2026"))
	f.Add([]byte{})

	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 4_096 {
			t.Skip()
		}
		matches := make([]Match, 0, len(data))
		for index := range data {
			if index%3 == 0 {
				matches = append(matches, Match{Id: 1, From: uint64(index), To: uint64(index + 1)})
			}
			if index+2 <= len(data) && index%5 == 0 {
				matches = append(matches, Match{Id: 2, From: uint64(index), To: uint64(index + 2)})
			}
		}
		want := append([]byte(nil), data...)
		for _, match := range resolveOverlappingMatches(append([]Match(nil), matches...)) {
			for index := match.From; index < match.To; index++ {
				want[index] = '#'
			}
		}
		Mask(data, matches, func(_ Match, value []byte) {
			for index := range value {
				value[index] = '#'
			}
		})
		if !bytes.Equal(data, want) {
			t.Fatalf("data = %q, want %q", data, want)
		}
	})
}

func equalMatches(got, want []Match) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range want {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}
