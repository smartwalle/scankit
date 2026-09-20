package teddy

import (
	"testing"

	"github.com/smartwalle/scankit/internal/hwlm"
)

func TestMatcherFindsBucketedCandidates(t *testing.T) {
	m := New([]hwlm.Literal{{ID: 1, Value: []byte("prefix-long")}, {ID: 2, Value: []byte("apple")}, {ID: 3, Value: []byte("ALP"), CaseInsensitive: true}})
	matches := m.Find([]byte("apple ALp prefix-long"))
	if len(matches) != 3 || matches[0].ID != 2 || matches[1].ID != 3 || matches[2].ID != 1 {
		t.Fatalf("unexpected bucket matches: %#v", matches)
	}
}

func TestMatcherRangeAndClone(t *testing.T) {
	m := New([]hwlm.Literal{{ID: 1, Value: []byte("aa")}})
	if matches := m.FindRange([]byte("aaaa"), 1, 4); len(matches) != 2 {
		t.Fatalf("range matches: %#v", matches)
	}
	clone := m.Clone()
	copy := clone.Literals()
	copy[0].Value[0] = 'z'
	if len(m.Find([]byte("aa"))) != 1 {
		t.Fatal("matcher clone leaked literals")
	}
}

func TestMatcherSerialization(t *testing.T) {
	m := New([]hwlm.Literal{{ID: 1, Value: []byte("needle")}})
	raw, err := m.Dump()
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(raw)
	if err != nil || len(loaded.Find([]byte("needle"))) != 1 {
		t.Fatalf("teddy serialization: %v", err)
	}
}
