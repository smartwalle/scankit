package util

import "container/heap"

// Queue 是可复用的 FIFO 队列。零值可直接使用。
type Queue[T any] struct {
	items []T
	head  int
}

// PriorityQueue 是支持稳定优先级出队的通用队列。
type PriorityQueue[T any] struct {
	items priorityItems[T]
	seq   uint64
}

type priorityItem[T any] struct {
	value    T
	priority int
	seq      uint64
}
type priorityItems[T any] []priorityItem[T]

func (p priorityItems[T]) Len() int { return len(p) }
func (p priorityItems[T]) Less(i, j int) bool {
	if p[i].priority != p[j].priority {
		return p[i].priority < p[j].priority
	}
	return p[i].seq < p[j].seq
}
func (p priorityItems[T]) Swap(i, j int) { p[i], p[j] = p[j], p[i] }
func (p *priorityItems[T]) Push(x any)   { *p = append(*p, x.(priorityItem[T])) }
func (p *priorityItems[T]) Pop() any {
	old := *p
	n := len(old)
	item := old[n-1]
	*p = old[:n-1]
	return item
}

// Push 按优先级加入元素，数值越小越早出队。
func (q *PriorityQueue[T]) Push(value T, priority int) {
	if q == nil {
		return
	}
	q.seq++
	heap.Push(&q.items, priorityItem[T]{value: value, priority: priority, seq: q.seq})
}

// Pop 返回当前优先级最高的元素。
func (q *PriorityQueue[T]) Pop() (T, int, bool) {
	var zero T
	if q == nil || len(q.items) == 0 {
		return zero, 0, false
	}
	item := heap.Pop(&q.items).(priorityItem[T])
	return item.value, item.priority, true
}

func (q *PriorityQueue[T]) Len() int {
	if q == nil {
		return 0
	}
	return len(q.items)
}
func (q *PriorityQueue[T]) Reset() {
	if q != nil {
		q.items = q.items[:0]
		q.seq = 0
	}
}

// Push 入队。
func (q *Queue[T]) Push(item T) {
	if q != nil {
		q.items = append(q.items, item)
	}
}

// Pop 出队；空队列返回 false。
func (q *Queue[T]) Pop() (T, bool) {
	if q == nil || q.head == len(q.items) {
		var zero T
		return zero, false
	}
	item := q.items[q.head]
	var zero T
	q.items[q.head] = zero
	q.head++
	if q.head == len(q.items) {
		q.Reset()
	}
	return item, true
}

// Len 返回待出队数量。
func (q Queue[T]) Len() int       { return len(q.items) - q.head }
func (q Queue[T]) Empty() bool    { return q.head >= len(q.items) }
func (q Queue[T]) HeadIndex() int { return q.head }
func (q *Queue[T]) Drain(dst []T) []T {
	for {
		v, ok := q.Pop()
		if !ok {
			return dst
		}
		dst = append(dst, v)
	}
}
func (q Queue[T]) Peek() (T, bool) {
	if q.head >= len(q.items) {
		var z T
		return z, false
	}
	return q.items[q.head], true
}
func (q Queue[T]) Cap() int { return cap(q.items) }

// Values 返回待出队元素的快照。
func (q Queue[T]) Values() []T {
	if q.head >= len(q.items) {
		return nil
	}
	return append([]T(nil), q.items[q.head:]...)
}

// PushFront 将元素插入队首。
func (q *Queue[T]) PushFront(item T) {
	if q == nil {
		return
	}
	if q.head > 0 {
		copy(q.items, q.items[q.head:])
		var zero T
		for i := len(q.items) - q.head; i < len(q.items); i++ {
			q.items[i] = zero
		}
		q.items = q.items[:len(q.items)-q.head]
		q.head = 0
	}
	q.items = append(q.items, item)
	copy(q.items[1:], q.items[:len(q.items)-1])
	q.items[0] = item
}

// PopN 最多出队 n 个元素并追加到 dst；n 为零时不限制。
func (q *Queue[T]) PopN(dst []T, n int) []T {
	if q == nil || n < 0 {
		return dst
	}
	count := 0
	for n == 0 || count < n {
		item, ok := q.Pop()
		if !ok {
			break
		}
		dst = append(dst, item)
		count++
	}
	return dst
}

// Compact 在已消费空间占比较大时将待处理元素移到队首。
func (q *Queue[T]) Compact() {
	if q == nil || q.head == 0 {
		return
	}
	if q.head < len(q.items)/2 {
		return
	}
	copy(q.items, q.items[q.head:])
	var zero T
	for i := len(q.items) - q.head; i < len(q.items); i++ {
		q.items[i] = zero
	}
	q.items = q.items[:len(q.items)-q.head]
	q.head = 0
}

// Reset 清空队列并保留容量。
func (q *Queue[T]) Reset() {
	var zero T
	for i := q.head; i < len(q.items); i++ {
		q.items[i] = zero
	}
	q.items = q.items[:0]
	q.head = 0
}
