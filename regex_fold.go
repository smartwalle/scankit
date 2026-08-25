package scankit

func foldRegexASCIIAtom(node *regexNode) {
	switch node.kind {
	case regexLiteral:
		if !isASCIILetter(node.literal) {
			return
		}
		var class byteClass
		class.add(node.literal)
		class.add(toggleASCIICase(node.literal))
		node.kind = regexClass
		node.class = class
	case regexClass:
		for value := byte('A'); value <= byte('Z'); value++ {
			if node.class.contains(value) || node.class.contains(value+'a'-'A') {
				node.class.add(value)
				node.class.add(value + 'a' - 'A')
			}
		}
	default:
	}
}

func isASCIILetter(value byte) bool {
	return value >= 'A' && value <= 'Z' || value >= 'a' && value <= 'z'
}

func toggleASCIICase(value byte) byte {
	if value >= 'A' && value <= 'Z' {
		return value + 'a' - 'A'
	}
	return value - 'a' + 'A'
}
