package scankit

import (
	"bytes"
	"testing"
)

var profileBenchmarkMatchesSink []Match

// BenchmarkProfileScanPaths 为 AC、锚点验证、无锚定 DFA 和事件收集提供可重复的定位入口。
// 它们不构成发布门槛，也不代表任何业务工作负载。
func BenchmarkProfileScanPaths(b *testing.B) {
	for _, test := range []struct {
		name        string
		expressions []Expression
		data        []byte
		want        []Match
	}{
		{
			name: "ACTraversal",
			expressions: []Expression{
				{Id: 1, Pattern: `metric-key-00`},
				{Id: 2, Pattern: `metric-key-01`},
				{Id: 3, Pattern: `metric-key-02`},
				{Id: 4, Pattern: `metric-key-03`},
			},
			data: bytes.Repeat([]byte(`metric-key-xx `), 256),
		},
		{
			name:        "AnchoredVerifier",
			expressions: []Expression{{Id: 1, Pattern: `account=[A-Z]{4}[0-9]{4}`}},
			data:        bytes.Repeat([]byte(`account=ABCD1234 `), 128),
			want:        repeatedProfileMatches(`account=ABCD1234 `, 0, 0, 16, 128, []uint32{1}),
		},
		{
			name:        "UnanchoredVerifierDFA",
			expressions: []Expression{{Id: 1, Pattern: `(?:ab|ac)[0-9]{1,2}`}},
			data:        bytes.Repeat([]byte(`ab12 `), 256),
			want:        repeatedProfileMatches(`ab12 `, 0, 3, 4, 256, []uint32{1}),
		},
		{
			name:        "EventCollection",
			expressions: profileEventCollectionExpressions(),
			data:        bytes.Repeat([]byte(`matched `), 64),
			want:        repeatedProfileMatches(`matched `, 0, 0, 7, 64, profileEventCollectionIDs()),
		},
	} {
		b.Run(test.name, func(b *testing.B) {
			scanner, err := Compile(test.expressions)
			if err != nil {
				b.Fatal(err)
			}
			benchmarkProfileScanInto(b, scanner, test.data, test.want)
		})
	}
}

func benchmarkProfileScanInto(b *testing.B, scanner *Scanner, data []byte, want []Match) {
	b.Helper()
	matches := make([]Match, 0, len(want))
	got, err := scanner.ScanInto(data, matches)
	if err != nil {
		b.Fatal(err)
	}
	if !equalMatches(got, want) {
		b.Fatalf("ScanInto() = %#v, want %#v", got, want)
	}
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		matches = matches[:0]
		matches, err = scanner.ScanInto(data, matches)
		if err != nil {
			b.Fatal(err)
		}
	}
	profileBenchmarkMatchesSink = matches
}

func repeatedProfileMatches(record string, from, minimumTo, maximumTo, count int, ids []uint32) []Match {
	matches := make([]Match, 0, count*len(ids))
	for index := range count {
		base := index * len(record)
		for _, id := range ids {
			if minimumTo != 0 {
				matches = append(matches, Match{Id: id, From: uint64(base + from), To: uint64(base + minimumTo)})
			}
			matches = append(matches, Match{Id: id, From: uint64(base + from), To: uint64(base + maximumTo)})
		}
	}
	return matches
}

func profileEventCollectionExpressions() []Expression {
	expressions := make([]Expression, 32)
	for index := range expressions {
		expressions[index] = Expression{Id: uint32(index + 1), Pattern: `matched`}
	}
	return expressions
}

func profileEventCollectionIDs() []uint32 {
	ids := make([]uint32, 32)
	for index := range ids {
		ids[index] = uint32(index + 1)
	}
	return ids
}
