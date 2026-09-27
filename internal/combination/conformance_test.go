package combination

import "testing"

func TestDefaultConformance(t *testing.T) {
	if err := RunConformance(DefaultConformanceCases()); err != nil {
		t.Fatal(err)
	}
}
