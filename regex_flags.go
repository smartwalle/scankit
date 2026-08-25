package scankit

// applyRegexFlags 在 NFA 编译前将表达式和内联语法标志下沉到面向字节的 AST。解析器保存
// 每个叶节点的有效标志，因此支持 (?-i:...) 等作用域形式。
func applyRegexFlags(node *regexNode, _ CompileFlag) {
	if node == nil {
		return
	}
	if node.kind == regexDot {
		class := allBytes()
		if node.flags&CompileDotAll == 0 {
			class.remove('\n')
		}
		node.kind = regexClass
		node.class = class
	}
	if node.flags&CompileCaseless != 0 {
		foldRegexASCIIAtom(node)
	}
	for _, child := range node.children {
		applyRegexFlags(child, 0)
	}
}
