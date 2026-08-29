package scankit

type CompileFlag uint32

type Expression struct {
	Id      uint32
	Pattern string
	Flags   CompileFlag
	Ext     *ExpressionExt
}

type ExpressionExtFlag uint64

type ExpressionExt struct {
	Flags           ExpressionExtFlag
	MinOffset       uint64
	MaxOffset       uint64
	MinLength       uint64
	EditDistance    uint32
	HammingDistance uint32
}

// Compile 编译全部表达式并返回不可变 Scanner。
func Compile(expressions []Expression) (*Scanner, error) {
	return nil, nil
}
