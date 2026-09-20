package parser

func Walk(root Node, fn func(Node) bool) {
	if root == nil || fn == nil || !fn(root) {
		return
	}
	switch v := root.(type) {
	case Group:
		Walk(v.Child, fn)
	case Lookaround:
		Walk(v.Child, fn)
	case Sequence:
		for _, c := range v.Elements {
			Walk(c, fn)
		}
	case Alternation:
		for _, c := range v.Options {
			Walk(c, fn)
		}
	case Repeat:
		Walk(v.Child, fn)
	case Conditional:
		Walk(v.Yes, fn)
		Walk(v.No, fn)
	case Combination:
		Walk(v.Expr, fn)
	case CombinationOperator:
		Walk(v.Left, fn)
		Walk(v.Right, fn)
	case CombinationNot:
		Walk(v.Child, fn)
	}
}
func NodeCount(root Node) int { n := 0; Walk(root, func(Node) bool { n++; return true }); return n }
