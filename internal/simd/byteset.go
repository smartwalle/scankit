package simd

// ByteSet 是预编译的 256 位字节集合。
//
// 直接对 [4]uint64 逐字节做位移与掩码判定无法被 SIMD 一次消化，因此在构建期
// 把集合按高半字节切成 16 行，每行保留低 8 位与高 8 位两张成员图。后端只要一次
// 向量查表就能判定窗口内每个字节是否属于该集合，热路径无需重复解析原始位图。
//
// 字段全部私有，避免调用方在构建后改写位图或查找表，导致标量路径与原生路径分叉。
type ByteSet struct {
	raw    [4]uint64
	tables [32]byte
	empty  bool
}

// NewByteSet 由 256 位位图构建预编译字节集合。
func NewByteSet(raw [4]uint64) ByteSet {
	set := ByteSet{raw: raw}
	set.empty = raw[0] == 0 && raw[1] == 0 && raw[2] == 0 && raw[3] == 0
	if set.empty {
		return set
	}
	for h := 0; h < 16; h++ {
		row := uint16(raw[h>>2] >> (uint(h&3) * 16))
		set.tables[h] = byte(row)
		set.tables[16+h] = byte(row >> 8)
	}
	return set
}

// TableVector 返回按半字节切分的查找表指针：前 16 字节是 lo 行表，后 16 字节是 hi 行表。
// 第 h 行对应高半字节为 h 的 16 个字节，lo[h] 是该行低 8 位的成员图，hi[h] 是高 8 位的成员图。
// 返回的内部数组指针仅供各后端原生内核只读查表，调用方不得改写。
func (s *ByteSet) TableVector() *[32]byte {
	if s == nil {
		return nil
	}
	return &s.tables
}

// Mask 使用可移植标量路径生成掩码，作为各后端原生路径的等价参考。
func (s ByteSet) Mask(v Vector) uint16 {
	if s.empty {
		return 0
	}
	var mask uint16
	for i, value := range v {
		if s.raw[value/64]&(1<<uint(value%64)) != 0 {
			mask |= 1 << uint(i)
		}
	}
	return mask
}

// ByteSetMaskPrepared 按预编译字节集合生成位置掩码。
//
// 与 ByteSetMask 相比，调用方只需在配置期构建一次 ByteSet，热路径即可复用
// 半字节查找表，避免每个窗口重复解析原始位图。
func (GenericBackend) ByteSetMaskPrepared(v Vector, set *ByteSet) uint16 {
	if set == nil {
		return 0
	}
	return set.Mask(v)
}

// ByteSetTables 保存最多 4 个 lane 的预编译查找表，按 lane 顺序连续存放。
//
// 每个 lane 占 32 字节，与 ByteSet 内部的张量布局一致：前 16 字节是低半字节行表，
// 后 16 字节是高半字节行表。使用定长数组而不是切片，原生内核即可用固定步长索引各
// lane 的表，无需在热路径上做边界判断。
type ByteSetTables [4][32]byte

// NewByteSetTables 由各 lane 的 256 位位图构建连续查找表。
// lane 数量少于 4 时其余 lane 保持空集合，语义等同“没有任何字节命中”。
func NewByteSetTables(sets [4][4]uint64) ByteSetTables {
	var tables ByteSetTables
	for lane, raw := range sets {
		tables[lane] = NewByteSet(raw).tables
	}
	return tables
}

// FirstByteTables 由单一 256 位首字节集合构建起点枚举用的 32 字节窗口查找表。
//
// 起点枚举只判断窗口首字节是否属于集合，因此只有 lane 0 携带集合，其余 lane 保持空集合，
// 调用方必须以 lanes 参数 1 使用该结果。传入空集合时得到全零表，与原始位图的
// “无候选”语义一致；空集合的判定仍由调用方在枚举入口单独处理。
func FirstByteTables(raw [4]uint64) ByteSetTables {
	return NewByteSetTables([4][4]uint64{raw})
}

// ClampLanes 把 lane 数裁剪到查找表支持的范围 [1, 4]。
func ClampLanes(lanes int) int {
	if lanes < 1 {
		return 1
	}
	if lanes > len(ByteSetTables{}) {
		return len(ByteSetTables{})
	}
	return lanes
}

// tableHas 判断字节是否属于某张半字节查找表描述的集合。
func tableHas(table *[32]byte, value byte) bool {
	// 高半字节选定行，低半字节的第 3 位选定该行的低 8 位或高 8 位成员图。
	index := int(value>>4) + 16*int((value>>3)&1)
	return table[index]&(1<<uint(value&7)) != 0
}

// ComposeWindowMask 由各 lane 的位置掩码组合出候选起点掩码。
//
// laneMasks[k] 的位 i 表示窗口偏移 i+k 的字节命中第 k 个 lane 集合；组合时右移 k 位
// 对齐到起点偏移。窗口末端缺少后继字节的位置退回首字节判定，保证不会漏报候选。
func ComposeWindowMask(laneMasks [4]uint32, lanes int) uint32 {
	lanes = ClampLanes(lanes)
	first := laneMasks[0]
	if lanes <= 1 {
		return first
	}
	strict := first
	for lane := 1; lane < lanes; lane++ {
		strict &= laneMasks[lane] >> uint(lane)
	}
	var boundary uint32
	for lane := 0; lane < lanes-1; lane++ {
		boundary |= 1 << uint(SuperWidth-1-lane)
	}
	return strict | first&boundary
}

// WindowMaskScalar 是 Backend.WindowMask 的可移植标量参考实现。
// 各后端在缺少原生能力时回退到该实现，保证原生与通用结果逐位一致。
func WindowMaskScalar(data []byte, off int, tables *ByteSetTables, lanes int) (uint32, bool) {
	if tables == nil || off < 0 || off > len(data) || len(data)-off < SuperWidth {
		return 0, false
	}
	lanes = ClampLanes(lanes)
	window := data[off : off+SuperWidth]
	var laneMasks [4]uint32
	for lane := 0; lane < lanes; lane++ {
		set := &tables[lane]
		var mask uint32
		for i, value := range window {
			if tableHas(set, value) {
				mask |= 1 << uint(i)
			}
		}
		laneMasks[lane] = mask
	}
	return ComposeWindowMask(laneMasks, lanes), true
}

// ComposeWindowMask64 由各 lane 的 64 位位置掩码组合出候选起点掩码。
//
// 语义与 ComposeWindowMask 完全一致，只是窗口宽度为 WideWidth：laneMasks[k] 的位 i
// 表示窗口偏移 i+k 的字节命中第 k 个 lane 集合，组合时右移 k 位对齐到起点偏移；
// 窗口末端缺少后继字节的位置退回首字节判定，保证不会漏报候选。
func ComposeWindowMask64(laneMasks [4]uint64, lanes int) uint64 {
	lanes = ClampLanes(lanes)
	first := laneMasks[0]
	if lanes <= 1 {
		return first
	}
	strict := first
	for lane := 1; lane < lanes; lane++ {
		strict &= laneMasks[lane] >> uint(lane)
	}
	var boundary uint64
	for lane := 0; lane < lanes-1; lane++ {
		boundary |= 1 << uint(WideWidth-1-lane)
	}
	return strict | first&boundary
}

// WindowMask64Scalar 是 Backend.WindowMask64 的可移植标量参考实现。
// 各后端在缺少原生能力时回退到该实现，保证原生与通用结果逐位一致。
func WindowMask64Scalar(data []byte, off int, tables *ByteSetTables, lanes int) (uint64, bool) {
	if tables == nil || off < 0 || off > len(data) || len(data)-off < WideWidth {
		return 0, false
	}
	lanes = ClampLanes(lanes)
	window := data[off : off+WideWidth]
	var laneMasks [4]uint64
	for lane := 0; lane < lanes; lane++ {
		set := &tables[lane]
		var mask uint64
		for i, value := range window {
			if tableHas(set, value) {
				mask |= 1 << uint(i)
			}
		}
		laneMasks[lane] = mask
	}
	return ComposeWindowMask64(laneMasks, lanes), true
}

// WindowMask64 按 64 字节宽窗口生成候选起点掩码。
//
// 契约与 WindowMask 一致，只是窗口宽度为 WideWidth：返回值的位 i 对应窗口内偏移 i
// 命中候选，数据不足一个完整宽窗口时返回 false，调用方应按字节回退判定。
func (GenericBackend) WindowMask64(data []byte, off int, tables *ByteSetTables, lanes int) (uint64, bool) {
	return WindowMask64Scalar(data, off, tables, lanes)
}

// WindowMask 按 32 字节窗口生成候选起点掩码，供 HWLM 热路径一次调用完成全部 lane 判定。
//
// 返回值的位 i 对应窗口内偏移 i 命中候选；数据不足一个完整窗口时返回 false，
// 调用方应按字节回退判定。位宽固定为 SuperWidth，因此调用方可以按 32 字节步进。
func (GenericBackend) WindowMask(data []byte, off int, tables *ByteSetTables, lanes int) (uint32, bool) {
	return WindowMaskScalar(data, off, tables, lanes)
}
