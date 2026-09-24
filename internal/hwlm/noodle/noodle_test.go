package noodle

import (
	"testing"

	"github.com/smartwalle/scankit/internal/hwlm"
)

func TestMatcherFindsSharedPrefixes(t *testing.T) {
	m := New([]hwlm.Literal{{ID: 1, Value: []byte("he")}, {ID: 2, Value: []byte("her")}, {ID: 3, Value: []byte("hers")}, {ID: 4, Value: []byte("SHE"), CaseInsensitive: true}})
	matches := m.Find([]byte("hers SHE"))
	if len(matches) != 4 || matches[0].ID != 1 || matches[1].ID != 2 || matches[2].ID != 3 || matches[3].ID != 4 {
		t.Fatalf("unexpected trie matches: %#v", matches)
	}
}

func TestMatcherRangeAndClone(t *testing.T) {
	m := New([]hwlm.Literal{{ID: 1, Value: []byte("aa")}})
	if matches := m.FindRange([]byte("aaaa"), 1, 4); len(matches) != 2 {
		t.Fatalf("range matches: %#v", matches)
	}
	clone := m.Clone()
	dup := clone.Literals()
	dup[0].Value[0] = 'z'
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
		t.Fatalf("noodle serialization: %v", err)
	}
}
