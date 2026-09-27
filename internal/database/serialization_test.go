package database

import (
	"runtime"
	"testing"
)

func TestMarshalRoundTrip(t *testing.T) {
	d := New([]string{"a", "b"})
	raw, e := d.Marshal()
	if e != nil {
		t.Fatal(e)
	}
	got, e := Unmarshal[string](raw)
	if e != nil || got.Len() != 2 {
		t.Fatal(e)
	}
}

func TestUnmarshalRejectsUnknownAndTrailingFields(t *testing.T) {
	for _, raw := range [][]byte{
		[]byte(`{"programs":[],"version":1,"architecture":"` + runtime.GOARCH + `","unknown":true}`),
		[]byte(`{"programs":[],"version":1,"architecture":"` + runtime.GOARCH + `"} {}`),
	} {
		if _, err := Unmarshal[string](raw); err == nil {
			t.Fatalf("无效数据库载荷未拒绝: %s", raw)
		}
	}
}

func TestDigestDetectsTamper(t *testing.T) {
	d := New([]int{1, 2})
	raw, err := d.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	for i := len(raw) - 1; i >= 0; i-- {
		if raw[i] == '1' {
			raw[i] = '9'
			break
		}
	}
	if _, err := Unmarshal[int](raw); err == nil {
		t.Fatal("篡改未被检测")
	}
}
func FuzzUnmarshal(f *testing.F) {
	f.Add([]byte(`{"version":1,"architecture":""}`))
	f.Fuzz(func(_ *testing.T, data []byte) { _, _ = Unmarshal[int](data) })
}
