package simd

import (
	"math/rand"
	"testing"

	"github.com/smartwalle/scankit/internal/simd/generic"
)

// bytesetReferenceSets 覆盖空集、全集、单字节、交替位与若干伪随机集合。
func bytesetReferenceSets() [][4]uint64 {
	sets := [][4]uint64{
		{},
		{^uint64(0), ^uint64(0), ^uint64(0), ^uint64(0)},
		{1, 0, 0, 0},
		{0, 0, 0, 1 << 63},
		{0xAAAAAAAAAAAAAAAA, 0x5555555555555555, 0xAAAAAAAAAAAAAAAA, 0x5555555555555555},
	}
	rng := rand.New(rand.NewSource(20260920))
	for i := 0; i < 64; i++ {
		var set [4]uint64
		for word := range set {
			set[word] = rng.Uint64()
		}
		sets = append(sets, set)
	}
	return sets
}

// TestByteSetMaskMatchesGeneric 验证预编译集合的标量结果与通用实现逐位一致。
func TestByteSetMaskMatchesGeneric(t *testing.T) {
	for _, raw := range bytesetReferenceSets() {
		set := NewByteSet(raw)
		want := generic.ByteSetMask(generic.Vector{}, raw)
		if got := set.Mask(generic.Vector{}); got != want {
			t.Fatalf("zero vector raw=%v got=%#x want=%#x", raw, got, want)
		}
		for b := 0; b < 256; b++ {
			var v Vector
			for i := range v {
				v[i] = byte((b + i*7) & 0xFF)
			}
			want := generic.ByteSetMask(v, raw)
			if got := set.Mask(v); got != want {
				t.Fatalf("raw=%v byte=%d got=%#x want=%#x", raw, b, got, want)
			}
			if got := (GenericBackend{}).ByteSetMaskPrepared(v, &set); got != want {
				t.Fatalf("prepared raw=%v byte=%d got=%#x want=%#x", raw, b, got, want)
			}
		}
	}
}

// TestByteSetEmptyAndSingleton 验证空集恒返回零掩码，单元素集合只命中对应字节。
func TestByteSetEmptyAndSingleton(t *testing.T) {
	var v Vector
	for i := range v {
		v[i] = byte(i * 7)
	}
	empty := NewByteSet([4]uint64{})
	if got := empty.Mask(v); got != 0 {
		t.Fatalf("空集掩码必须为零, got %#x", got)
	}
	if got := empty.Mask(Vector{}); got != 0 {
		t.Fatalf("空集对零向量掩码必须为零, got %#x", got)
	}
	set := NewByteSet([4]uint64{0, 1, 0, 0})
	if got := set.Mask(v); got != 0 {
		t.Fatalf("不含 64 时掩码必须为零, got %#x", got)
	}
	v[3] = 64
	if got := set.Mask(v); got != 1<<3 {
		t.Fatalf("含 64 时应只命中第 3 个 lane, got %#x", got)
	}
	v[3] = 192
	if got := set.Mask(v); got != 0 {
		t.Fatalf("192 不在集合内, got %#x", got)
	}
}

// BenchmarkByteSetMaskPrepared 对比预编译集合与原始位图在热路径上的开销。
func BenchmarkByteSetMaskPrepared(b *testing.B) {
	raw := [4]uint64{0x0000000000000000, 0x03FF000000000000, 0x7FFFFFE00000000, 0}
	set := NewByteSet(raw)
	backend := GenericBackend{}
	var v Vector
	for i := range v {
		v[i] = byte('a' + i%26)
	}
	b.Run("raw", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if backend.ByteSetMask(v, raw) == 0 {
				b.Fatal("unexpected empty mask")
			}
		}
	})
	b.Run("prepared", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if backend.ByteSetMaskPrepared(v, &set) == 0 {
				b.Fatal("unexpected empty mask")
			}
		}
	})
}

// windowMaskNaive 逐位置独立判定候选起点，作为窗口掩码组合规则的参考实现。
// 与后端实现的区别在于它不做任何按 lane 的对齐，便于交叉验证掩码位序。
func windowMaskNaive(data []byte, off int, tables *ByteSetTables, lanes int) (uint32, bool) {
	if tables == nil || off < 0 || off > len(data) || len(data)-off < SuperWidth {
		return 0, false
	}
	lanes = ClampLanes(lanes)
	var mask uint32
	for i := 0; i+lanes <= SuperWidth; i++ {
		hit := true
		for lane := 0; lane < lanes; lane++ {
			if !tableHas(&tables[lane], data[off+i+lane]) {
				hit = false
				break
			}
		}
		if hit {
			mask |= 1 << uint(i)
		}
	}
	// 窗口末端缺少 lanes-1 个后继字节的位置退回首字节判定。
	for i := SuperWidth - (lanes - 1); i < SuperWidth; i++ {
		if tableHas(&tables[0], data[off+i]) {
			mask |= 1 << uint(i)
		}
	}
	return mask, true
}

// TestWindowMaskScalarMatchesNaiveReference 验证窗口掩码的严格判定、末端退让、
// lane 数量裁剪与边界输入都与逐位置参考实现一致。
func TestWindowMaskScalarMatchesNaiveReference(t *testing.T) {
	rng := rand.New(rand.NewSource(20260921))
	pool := bytesetReferenceSets()
	for iter := 0; iter < 128; iter++ {
		var laneSets [4][4]uint64
		for lane := range laneSets {
			laneSets[lane] = pool[rng.Intn(len(pool))]
		}
		tables := NewByteSetTables(laneSets)
		data := make([]byte, SuperWidth*2)
		for i := range data {
			data[i] = byte(rng.Intn(256))
		}
		for _, off := range []int{0, 1, 3, SuperWidth - 1} {
			for lanes := 1; lanes <= 4; lanes++ {
				got, ok := WindowMaskScalar(data, off, &tables, lanes)
				want, wantOK := windowMaskNaive(data, off, &tables, lanes)
				if ok != wantOK || got != want {
					t.Fatalf("迭代 %d 偏移 %d lane %d: got=%032b want=%032b ok=%v", iter, off, lanes, got, want, ok)
				}
				if backend, okBackend := (GenericBackend{}).WindowMask(data, off, &tables, lanes); okBackend != ok || backend != got {
					t.Fatalf("迭代 %d 偏移 %d lane %d: 通用后端与标量参考不一致", iter, off, lanes)
				}
			}
		}
	}
}

// TestWindowMaskScalarEdgeCases 覆盖空集合、空指针、越界偏移与 lane 数越界。
func TestWindowMaskScalarEdgeCases(t *testing.T) {
	data := make([]byte, SuperWidth)
	for i := range data {
		data[i] = 'a'
	}
	var set [4]uint64
	set['a'/64] |= 1 << ('a' % 64)
	var laneSets [4][4]uint64
	for lane := range laneSets {
		laneSets[lane] = set
	}
	tables := NewByteSetTables(laneSets)
	for _, tc := range []struct {
		name   string
		off    int
		data   []byte
		tables *ByteSetTables
		lanes  int
		ok     bool
		want   uint32
	}{
		{"nil_tables", 0, data, nil, 1, false, 0},
		{"negative_offset", -1, data, &tables, 1, false, 0},
		{"short_data", 0, data[:SuperWidth-1], &tables, 1, false, 0},
		{"offset_at_end", SuperWidth, data, &tables, 1, false, 0},
		{"single_lane", 0, data, &tables, 1, true, ^uint32(0)},
		{"lane_below_range", 0, data, &tables, 0, true, ^uint32(0)},
		{"lane_above_range", 0, data, &tables, 9, true, ^uint32(0)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := WindowMaskScalar(tc.data, tc.off, tc.tables, tc.lanes)
			if ok != tc.ok {
				t.Fatalf("ok 不一致: got=%v want=%v", ok, tc.ok)
			}
			if ok && got != tc.want {
				t.Fatalf("掩码不一致: got=%032b want=%032b", got, tc.want)
			}
		})
	}
	empty := NewByteSetTables([4][4]uint64{})
	if got, ok := WindowMaskScalar(data, 0, &empty, 4); !ok || got != 0 {
		t.Fatalf("空集合应返回零掩码: got=%032b ok=%v", got, ok)
	}
}

// BenchmarkWindowMaskScalar 与 BenchmarkComposeWindowMask 记录窗口判定的标量成本，
// 供原生后端在相同窗口语义下对照。
func BenchmarkWindowMaskScalar(b *testing.B) {
	var laneSets [4][4]uint64
	for lane := 0; lane < 4; lane++ {
		for value := 0; value < 256; value += 3 {
			laneSets[lane][value/64] |= 1 << uint(value%64)
		}
	}
	tables := NewByteSetTables(laneSets)
	data := make([]byte, SuperWidth*64)
	for i := range data {
		data[i] = byte(i * 37)
	}
	for _, lanes := range []int{1, 4} {
		b.Run(map[int]string{1: "lanes1", 4: "lanes4"}[lanes], func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(data)))
			var acc uint32
			for i := 0; i < b.N; i++ {
				for off := 0; off+SuperWidth <= len(data); off += SuperWidth {
					mask, ok := WindowMaskScalar(data, off, &tables, lanes)
					if !ok {
						b.Fatal("窗口掩码不应越界")
					}
					acc |= mask
				}
			}
			if acc == 0 {
				b.Fatal("掩码不应全为零")
			}
		})
	}
}

// TestFirstByteTablesOnlyFillsFirstLane 校验单集合窗口表只填充 lane 0，
// 其余 lane 保持空集合，配合 lanes=1 使用时等价于逐字节集合判定。
func TestFirstByteTablesOnlyFillsFirstLane(t *testing.T) {
	var zero [32]byte
	for _, set := range bytesetReferenceSets() {
		tables := FirstByteTables(set)
		for lane := 1; lane < len(ByteSetTables{}); lane++ {
			if tables[lane] != zero {
				t.Fatalf("lane %d 非空: %v", lane, tables[lane])
			}
		}
		for value := 0; value < 256; value++ {
			member := set[value/64]>>uint(value%64)&1 == 1
			if got := tableHas(&tables[0], byte(value)); got != member {
				t.Fatalf("集合=%v 字节=%d 表判定=%v 位图=%v", set, value, got, member)
			}
		}
		window := make([]byte, SuperWidth)
		for off := range window {
			window[off] = byte((off * 37) % 256)
		}
		mask, ok := WindowMaskScalar(window, 0, &tables, 1)
		if !ok {
			t.Fatal("完整窗口应返回有效掩码")
		}
		for off, value := range window {
			member := set[value/64]>>uint(value%64)&1 == 1
			if got := mask>>uint(off)&1 == 1; got != member {
				t.Fatalf("集合=%v 偏移=%d 字节=%d 掩码位=%v 位图=%v", set, off, value, got, member)
			}
		}
	}
}

// windowMask64Naive 逐位置独立判定 64 字节宽窗口的候选起点，作为宽窗口组合规则的参考实现。
// 与后端实现的区别在于它不做任何按 lane 的对齐，便于交叉验证掩码位序与末端退让。
func windowMask64Naive(data []byte, off int, tables *ByteSetTables, lanes int) (uint64, bool) {
	if tables == nil || off < 0 || off > len(data) || len(data)-off < WideWidth {
		return 0, false
	}
	lanes = ClampLanes(lanes)
	var mask uint64
	for i := 0; i+lanes <= WideWidth; i++ {
		hit := true
		for lane := 0; lane < lanes; lane++ {
			if !tableHas(&tables[lane], data[off+i+lane]) {
				hit = false
				break
			}
		}
		if hit {
			mask |= 1 << uint(i)
		}
	}
	// 窗口末端缺少 lanes-1 个后继字节的位置退回首字节判定。
	for i := WideWidth - (lanes - 1); i < WideWidth; i++ {
		if tableHas(&tables[0], data[off+i]) {
			mask |= 1 << uint(i)
		}
	}
	return mask, true
}

// TestWindowMask64ScalarMatchesNaiveReference 验证 64 字节宽窗口掩码的严格判定、
// 末端退让、lane 数量裁剪与边界输入都与逐位置参考实现一致。
func TestWindowMask64ScalarMatchesNaiveReference(t *testing.T) {
	rng := rand.New(rand.NewSource(20260925))
	pool := bytesetReferenceSets()
	for iter := 0; iter < 128; iter++ {
		var laneSets [4][4]uint64
		for lane := range laneSets {
			laneSets[lane] = pool[rng.Intn(len(pool))]
		}
		tables := NewByteSetTables(laneSets)
		data := make([]byte, WideWidth*2)
		for i := range data {
			data[i] = byte(rng.Intn(256))
		}
		// 偏移 0/1/3 覆盖非对齐起点，WideWidth-1 之后不足一个宽窗口必须被拒绝。
		for _, off := range []int{0, 1, 3, WideWidth - 1, WideWidth} {
			for lanes := 1; lanes <= 4; lanes++ {
				got, ok := WindowMask64Scalar(data, off, &tables, lanes)
				want, wantOK := windowMask64Naive(data, off, &tables, lanes)
				if ok != wantOK || got != want {
					t.Fatalf("迭代 %d 偏移 %d lane %d: got=%064b ok=%v want=%064b wantOK=%v",
						iter, off, lanes, got, ok, want, wantOK)
				}
				if backend, okBackend := (GenericBackend{}).WindowMask64(data, off, &tables, lanes); okBackend != ok || backend != got {
					t.Fatalf("迭代 %d 偏移 %d lane %d: 通用后端与标量参考不一致", iter, off, lanes)
				}
			}
		}
	}
}

// TestWindowMask64ScalarEdgeCases 覆盖空集合、空指针、越界偏移、越界 lane 数与
// 恰好一个宽窗口的数据长度。
func TestWindowMask64ScalarEdgeCases(t *testing.T) {
	data := make([]byte, WideWidth)
	for i := range data {
		data[i] = 'a'
	}
	var set [4]uint64
	set['a'/64] |= 1 << ('a' % 64)
	var laneSets [4][4]uint64
	for lane := range laneSets {
		laneSets[lane] = set
	}
	tables := NewByteSetTables(laneSets)
	for _, tc := range []struct {
		name   string
		off    int
		data   []byte
		tables *ByteSetTables
		lanes  int
		ok     bool
		want   uint64
	}{
		{"nil_tables", 0, data, nil, 1, false, 0},
		{"negative_offset", -1, data, &tables, 1, false, 0},
		{"short_data", 0, data[:WideWidth-1], &tables, 1, false, 0},
		{"offset_at_end", WideWidth, data, &tables, 1, false, 0},
		{"single_lane", 0, data, &tables, 1, true, ^uint64(0)},
		{"lane_below_range", 0, data, &tables, 0, true, ^uint64(0)},
		{"lane_above_range", 0, data, &tables, 9, true, ^uint64(0)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := WindowMask64Scalar(tc.data, tc.off, tc.tables, tc.lanes)
			if ok != tc.ok {
				t.Fatalf("ok 不一致: got=%v want=%v", ok, tc.ok)
			}
			if ok && got != tc.want {
				t.Fatalf("掩码不一致: got=%064b want=%064b", got, tc.want)
			}
		})
	}
	empty := NewByteSetTables([4][4]uint64{})
	if got, ok := WindowMask64Scalar(data, 0, &empty, 4); !ok || got != 0 {
		t.Fatalf("空集合应返回零掩码: got=%064b ok=%v", got, ok)
	}
}

// TestWindowMask64WithFirstByteTables 校验单集合宽窗口表只填充 lane 0，
// 其余 lane 保持空集合，配合 lanes=1 使用时等价于逐字节集合判定。
func TestWindowMask64WithFirstByteTables(t *testing.T) {
	var zero [32]byte
	for _, set := range bytesetReferenceSets() {
		tables := FirstByteTables(set)
		for lane := 1; lane < len(ByteSetTables{}); lane++ {
			if tables[lane] != zero {
				t.Fatalf("lane %d 非空: %v", lane, tables[lane])
			}
		}
		window := make([]byte, WideWidth)
		for i := range window {
			window[i] = byte((i * 37) % 256)
		}
		mask, ok := WindowMask64Scalar(window, 0, &tables, 1)
		if !ok {
			t.Fatal("完整宽窗口应返回有效掩码")
		}
		for off, value := range window {
			member := set[value/64]>>uint(value%64)&1 == 1
			if got := mask>>uint(off)&1 == 1; got != member {
				t.Fatalf("集合=%v 偏移=%d 字节=%d 掩码位=%v 位图=%v", set, off, value, got, member)
			}
		}
	}
}

// BenchmarkWindowMask64Scalar 记录 64 字节宽窗口标量判定的单窗口成本，
// 供原生后端在相同窗口语义下对照。
func BenchmarkWindowMask64Scalar(b *testing.B) {
	var laneSets [4][4]uint64
	for lane := 0; lane < 4; lane++ {
		for value := 0; value < 256; value += 3 {
			laneSets[lane][value/64] |= 1 << uint(value%64)
		}
	}
	tables := NewByteSetTables(laneSets)
	data := make([]byte, WideWidth*64)
	for i := range data {
		data[i] = byte(i * 37)
	}
	for _, lanes := range []int{1, 4} {
		b.Run(map[int]string{1: "lanes1", 4: "lanes4"}[lanes], func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(data)))
			var acc uint64
			for i := 0; i < b.N; i++ {
				for off := 0; off+WideWidth <= len(data); off += WideWidth {
					mask, ok := WindowMask64Scalar(data, off, &tables, lanes)
					if !ok {
						b.Fatal("窗口掩码不应越界")
					}
					acc |= mask
				}
			}
			if acc == 0 {
				b.Fatal("掩码不应全为零")
			}
		})
	}
}
