package scankit

func programContainsAnchor(program nfaProgram) bool {
	for _, state := range program.states {
		if state.kind == nfaAnchorStart || state.kind == nfaAnchorEnd || state.kind == nfaAbsoluteStart || state.kind == nfaAbsoluteEnd || state.kind == nfaEndBeforeFinalNewline || state.kind == nfaWordBoundary || state.kind == nfaNotWordBoundary || state.kind == nfaLineBreak || state.kind == nfaLineBreakCR {
			return true
		}
	}
	return false
}
