package scankit

import "testing"

func TestDirectEventDeliveryKeepsFallbackSemantics(t *testing.T) {
	t.Parallel()
	direct, err := Compile([]Expression{{Id: 1, Pattern: `ab`}, {Id: 2, Pattern: `bc`}})
	if err != nil {
		t.Fatal(err)
	}
	fallback := *direct
	fallback.directSingleEvent = false
	data := []byte(`zabcab`)
	got, err := direct.Scan(data)
	if err != nil {
		t.Fatal(err)
	}
	want, err := fallback.Scan(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("direct=%#v fallback=%#v", got, want)
	}
	for index := range got {
		if got[index] != want[index] {
			t.Fatalf("match %d direct=%#v fallback=%#v", index, got[index], want[index])
		}
	}
}

func TestDirectEventDeliveryKeepsMultipleSameEndEvents(t *testing.T) {
	t.Parallel()
	direct, err := Compile([]Expression{{Id: 1, Pattern: `ab`}, {Id: 2, Pattern: `b`}})
	if err != nil {
		t.Fatal(err)
	}
	fallback := *direct
	fallback.directSingleEvent = false
	data := []byte(`ab`)
	got, err := direct.Scan(data)
	if err != nil {
		t.Fatal(err)
	}
	want, err := fallback.Scan(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("direct=%#v fallback=%#v", got, want)
	}
	for index := range got {
		if got[index] != want[index] {
			t.Fatalf("match %d direct=%#v fallback=%#v", index, got[index], want[index])
		}
	}
}

func TestDirectEventDeliveryIgnoresInactiveQuietExpression(t *testing.T) {
	t.Parallel()
	direct, err := Compile([]Expression{
		{Id: 1, Pattern: `secret`, Flags: CompileQuiet},
		{Id: 2, Pattern: `visible`},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !direct.directSingleEvent {
		t.Fatal("inactive QUIET expression disabled direct event delivery")
	}
	fallback := *direct
	fallback.directSingleEvent = false
	data := []byte(`secret visible secret`)
	got, err := direct.Scan(data)
	if err != nil {
		t.Fatal(err)
	}
	want, err := fallback.Scan(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(want) {
		t.Fatalf("direct=%#v fallback=%#v", got, want)
	}
	for index := range got {
		if got[index] != want[index] {
			t.Fatalf("match %d direct=%#v fallback=%#v", index, got[index], want[index])
		}
	}
}

func FuzzDirectEventDeliveryMatchesFallback(f *testing.F) {
	direct, err := Compile([]Expression{{Id: 1, Pattern: `ab`}, {Id: 2, Pattern: `bc`}})
	if err != nil {
		f.Fatal(err)
	}
	f.Add([]byte(`zabcab`))
	f.Add([]byte{})
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 4_096 {
			t.Skip()
		}
		fallback := *direct
		fallback.directSingleEvent = false
		got, err := direct.ScanInto(data, nil)
		if err != nil {
			t.Fatal(err)
		}
		want, err := fallback.ScanInto(data, nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) != len(want) {
			t.Fatalf("direct=%#v fallback=%#v", got, want)
		}
		for index := range got {
			if got[index] != want[index] {
				t.Fatalf("match %d direct=%#v fallback=%#v", index, got[index], want[index])
			}
		}
	})
}
