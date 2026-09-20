// Package contract 保存公开扫描接口的稳定契约快照。
package contract

// FlagValue 描述一个编译标志的名称和值。
type FlagValue struct {
	Name  string
	Value uint32
}

// CompileFlags 返回公开编译标志的稳定快照。
func CompileFlags() []FlagValue {
	return []FlagValue{
		{Name: "caseless", Value: 1}, {Name: "dotall", Value: 2},
		{Name: "multiline", Value: 4}, {Name: "singlematch", Value: 8},
		{Name: "allowempty", Value: 16}, {Name: "utf8", Value: 32},
		{Name: "ucp", Value: 64}, {Name: "prefilter", Value: 128},
		{Name: "som-leftmost", Value: 256}, {Name: "combination", Value: 512},
		{Name: "quiet", Value: 1024},
	}
}

// ExtensionFlags 返回表达式扩展标志的稳定快照。
func ExtensionFlags() []FlagValue {
	return []FlagValue{
		{Name: "min-offset", Value: 1}, {Name: "max-offset", Value: 2},
		{Name: "min-length", Value: 4}, {Name: "edit-distance", Value: 8},
		{Name: "hamming-distance", Value: 16},
	}
}

// MatchOrdering 描述结果字段；Scanner 不承诺对结果重新排序。
func MatchOrdering() []string { return []string{"producer_order"} }

// OffsetUnit 返回公开偏移的单位。
func OffsetUnit() string { return "byte" }
