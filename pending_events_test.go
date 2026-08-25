package scankit

import (
	"slices"
	"testing"
)

func TestPendingEventQueuePreservesOrderAcrossFIFOAndHeapModes(t *testing.T) {
	ctx := context{pendingFIFO: true}
	input := []scanEvent{
		{match: Match{From: 0, To: 4}, expressionIndex: 2},
		{match: Match{From: 0, To: 7}, expressionIndex: 1},
		{match: Match{From: 0, To: 9}, expressionIndex: 3},
	}
	for _, event := range input {
		ctx.pushPendingEvent(event)
	}
	if !ctx.pendingFIFO {
		t.Fatal("ordered events unexpectedly left FIFO mode")
	}
	if got := ctx.popPendingEvent(); !pendingEventsEqual(got, input[0]) {
		t.Fatalf("first pending event = %#v, want %#v", got, input[0])
	}
	inserted := scanEvent{match: Match{From: 0, To: 6}, expressionIndex: 4}
	ctx.pushPendingEvent(inserted)
	if ctx.pendingFIFO {
		t.Fatal("out-of-order event did not enable heap mode")
	}

	want := append([]scanEvent(nil), input[1:]...)
	want = append(want, inserted)
	slices.SortFunc(want, comparePendingEvents)
	got := make([]scanEvent, 0, len(want))
	for !ctx.pendingEmpty() {
		got = append(got, ctx.popPendingEvent())
	}
	if !slices.EqualFunc(got, want, pendingEventsEqual) {
		t.Fatalf("pending order = %#v, want %#v", got, want)
	}
}

func FuzzPendingEventQueuePreservesOrder(f *testing.F) {
	f.Add([]byte{4, 7, 6, 9})
	f.Add([]byte{9, 8, 7, 6})
	f.Add([]byte{1, 1, 1, 1})

	f.Fuzz(func(t *testing.T, encoded []byte) {
		if len(encoded) > 256 {
			t.Skip()
		}
		ctx := context{pendingFIFO: true}
		want := make([]scanEvent, 0, len(encoded))
		for index, value := range encoded {
			event := scanEvent{match: Match{From: uint64(index % 3), To: uint64(value)}, expressionIndex: uint32(index % 5)}
			ctx.pushPendingEvent(event)
			want = append(want, event)
		}
		slices.SortFunc(want, comparePendingEvents)
		got := make([]scanEvent, 0, len(want))
		for !ctx.pendingEmpty() {
			got = append(got, ctx.popPendingEvent())
		}
		if !slices.EqualFunc(got, want, pendingEventsEqual) {
			t.Fatalf("pending order = %#v, want %#v", got, want)
		}
	})
}

func comparePendingEvents(left, right scanEvent) int {
	switch {
	case scanEventComesBefore(left, right):
		return -1
	case scanEventComesBefore(right, left):
		return 1
	default:
		return 0
	}
}

func pendingEventsEqual(left, right scanEvent) bool {
	return left.expressionIndex == right.expressionIndex && left.match == right.match
}
