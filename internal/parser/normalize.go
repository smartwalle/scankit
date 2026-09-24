package parser

import "fmt"

// Normalize 归一化 AST：合并相邻字符类与文字、消除空结构，保持匹配语义不变。
func Normalize(root Node) Node {
	switch v := root.(type) {
	case Class:
		if len(v.Ranges) < 2 {
			return v
		}
		ranges := append([]Range(nil), v.Ranges...)
		for i := 1; i < len(ranges); i++ {
			for j := i; j > 0 && ranges[j].Lo < ranges[j-1].Lo; j-- {
				ranges[j], ranges[j-1] = ranges[j-1], ranges[j]
			}
		}
		out := ranges[:1]
		for _, r := range ranges[1:] {
			last := &out[len(out)-1]
			if int(r.Lo) <= int(last.Hi)+1 {
				if r.Hi > last.Hi {
					last.Hi = r.Hi
				}
			} else {
				out = append(out, r)
			}
		}
		v.Ranges = out
		return v
	case Sequence:
		var out []Node
		var buf []byte
		flush := func() {
			if len(buf) > 0 {
				out = append(out, Literal{Value: append([]byte(nil), buf...)})
				buf = nil
			}
		}
		var elements []Node
		for _, c := range v.Elements {
			if nested, ok := c.(Sequence); ok {
				elements = append(elements, nested.Elements...)
			} else {
				elements = append(elements, c)
			}
		}
		for _, c := range elements {
			if l, ok := c.(Literal); ok {
				buf = append(buf, l.Value...)
			} else {
				flush()
				out = append(out, Normalize(c))
			}
		}
		flush()
		if len(out) == 1 {
			return out[0]
		}
		return Sequence{Elements: out}
	case Group:
		v.Child = Normalize(v.Child)
		return v
	case Alternation:
		var flat []Node
		for _, c := range v.Options {
			if nested, ok := c.(Alternation); ok {
				flat = append(flat, nested.Options...)
			} else {
				flat = append(flat, c)
			}
		}
		v.Options = flat
		for i, c := range v.Options {
			v.Options[i] = Normalize(c)
		}
		unique := make([]Node, 0, len(v.Options))
		seen := map[string]struct{}{}
		for _, option := range v.Options {
			key := nodeKey(option)
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			unique = append(unique, option)
		}
		v.Options = unique
		if len(v.Options) == 1 {
			return v.Options[0]
		}
		return v
	case Repeat:
		v.Child = Normalize(v.Child)
		if v.Min == 1 && v.Max == 1 {
			return v.Child
		}
		return v
	case Lookaround:
		v.Child = Normalize(v.Child)
		return v
	default:
	}
	return root
}

func nodeKey(n Node) string {
	switch v := n.(type) {
	case Literal:
		return "l:" + string(v.Value)
	case Class:
		return "c:" + string([]byte{boolByte(v.Negated), byte(v.Kind)}) + fmt.Sprint(v.Ranges)
	default:
		return fmt.Sprintf("%T:%v", n, n)
	}
}

func boolByte(v bool) byte {
	if v {
		return 1
	}
	return 0
}
