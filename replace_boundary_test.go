package scankit

import (
	"bytes"
	"testing"
)

func TestReplaceIgnoresInvalidOffsets(t *testing.T) {
	data := []byte("abc")
	got := Replace(data, []Match{{From: 9, To: 10}, {From: 2, To: 1}}, func(b *bytes.Buffer, _ Match, _ []byte) { b.WriteString("x") })
	if string(got) != "abc" {
		t.Fatal(string(got))
	}
}
