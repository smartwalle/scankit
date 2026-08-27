package scankit

// context 持有 Scanner 内部复用的可变扫描状态，不属于扫描 API，且不得在一次活动扫描外保留。
type context struct {
	state              uint32
	regexVerifiers     []*nfaVerifierContext
	regexRunners       []*nfaSchedulerContext
	regexRepeats       []byteRegexRepeatRun
	regexPrefixRepeats []byteRegexPrefixRepeatRun
	regexFixed         []fixedByteRegexRun
	regexBounded       []fixedByteRegexRun
	unicodeRuns        []unicodePropertyRun
	unicodeApprox      []unicodeApproximateRun
	singleMatched      []bool
	pendingEvents      []scanEvent
	// 当生产者按自然扫描顺序发现未来事件时，pendingFIFO 保持为 true。常见锚定正则路径
	// 具有该性质，因此可无需通用 NFA/定宽执行器所需的堆维护而出队。
	pendingFIFO     bool
	pendingHead     int
	pendingCount    int
	pendingFirstEnd uint64
	readyEvents     []scanEvent
	combinationSeen []bool
	combinationOn   []bool
	editPrevious    []uint16
	editCurrent     []uint16
	hammingNFA      []nfaHammingContext
	editNFA         []nfaEditContext
}

// scanEvent 是 context 暂存的内部匹配候选，直至解决其顺序和组合约束。
type scanEvent struct {
	match           Match
	expressionIndex uint32
	anchorStart     bool
	anchorEnd       bool
	wordStart       bool
	wordEnd         bool
}

// newContext 为 scanner 的内部池创建一个可变扫描上下文。
func (scanner *Scanner) newContext() *context {
	if scanner == nil {
		return nil
	}
	ctx := &context{
		pendingEvents: make([]scanEvent, 0, scanner.eventCapacity),
		pendingFIFO:   true,
		readyEvents:   make([]scanEvent, 0, scanner.eventCapacity),
	}
	if len(scanner.expressions) != 0 {
		ctx.singleMatched = make([]bool, len(scanner.expressions))
	}
	if len(scanner.combinations) != 0 {
		ctx.combinationSeen = make([]bool, len(scanner.expressions))
		ctx.combinationOn = make([]bool, len(scanner.expressions))
	}
	if scanner.maxEditLength != 0 {
		ctx.editPrevious = make([]uint16, scanner.maxEditLength+1)
		ctx.editCurrent = make([]uint16, scanner.maxEditLength+1)
	}
	if len(scanner.hammingNFAs) != 0 {
		ctx.hammingNFA = make([]nfaHammingContext, len(scanner.hammingNFAs))
		for index, program := range scanner.hammingNFAs {
			ctx.hammingNFA[index] = newNFAHammingContext(program.program, program.distance)
		}
	}
	if len(scanner.editNFAs) != 0 {
		ctx.editNFA = make([]nfaEditContext, len(scanner.editNFAs))
		for index, program := range scanner.editNFAs {
			ctx.editNFA[index] = newNFAEditContext(program.program, program.distance)
		}
	}
	if len(scanner.unicodeProperties) != 0 || len(scanner.unicodeApproximate) != 0 {
		ctx.unicodeRuns = make([]unicodePropertyRun, len(scanner.unicodeProperties))
		for index, program := range scanner.unicodeProperties {
			if len(program.sequence) != 0 {
				ctx.unicodeRuns[index].sequence = make([]unicodePropertyRune, 0, len(program.sequence))
			}
			if program.graph != nil {
				capacity := len(program.graph.states)
				if capacity < 4 {
					capacity = 4
				}
				ctx.unicodeRuns[index].active = make([]unicodePropertyState, 0, capacity)
				ctx.unicodeRuns[index].next = make([]unicodePropertyState, 0, capacity)
				ctx.unicodeRuns[index].graphActiveSeen = make([]uint32, capacity)
				if program.hasAssertions {
					for slot := range ctx.unicodeRuns[index].graphSeen {
						ctx.unicodeRuns[index].graphSeen[slot] = make([]uint32, capacity)
						ctx.unicodeRuns[index].graphStack[slot] = make([]uint16, 0, capacity)
					}
				}
			} else if program.runeNFA {
				// 短有界属性序列通常只有少量活跃状态。在创建 ctx 时预留工作集，避免首次块扫描
				// 扩容状态切片。
				capacity := len(program.sequence) * 4
				ctx.unicodeRuns[index].active = make([]unicodePropertyState, 0, capacity)
				ctx.unicodeRuns[index].next = make([]unicodePropertyState, 0, capacity)
			}
		}
		ctx.unicodeApprox = make([]unicodeApproximateRun, len(scanner.unicodeApproximate))
		for index, program := range scanner.unicodeApproximate {
			capacity := len(program.atoms)
			if program.graph != nil {
				capacity = program.maximumWidth
			}
			if !program.hamming {
				capacity += int(program.distance)
			}
			if capacity < 1 {
				capacity = 1
			}
			ctx.unicodeApprox[index].runes = make([]unicodePropertyRune, 0, capacity)
			if program.graph != nil {
				ctx.unicodeApprox[index].graphProduct = newUnicodeGraphApproximateContext(program.graph, program.distance)
			} else if !program.hamming {
				ctx.unicodeApprox[index].previous = make([]uint16, len(program.atoms)+1)
				ctx.unicodeApprox[index].current = make([]uint16, len(program.atoms)+1)
			}
		}
	}
	if len(scanner.regexPrograms) != 0 {
		ctx.regexVerifiers = make([]*nfaVerifierContext, len(scanner.regexPrograms))
		ctx.regexRunners = make([]*nfaSchedulerContext, len(scanner.regexPrograms))
		ctx.regexRepeats = make([]byteRegexRepeatRun, len(scanner.regexPrograms))
		ctx.regexPrefixRepeats = make([]byteRegexPrefixRepeatRun, len(scanner.regexPrograms))
		ctx.regexFixed = make([]fixedByteRegexRun, len(scanner.regexPrograms))
		ctx.regexBounded = make([]fixedByteRegexRun, len(scanner.regexPrograms))
		for groupIndex, group := range scanner.anchoredGroups {
			if !scanner.anchoredNeeded[groupIndex] {
				continue
			}
			representative := group[0]
			program := scanner.regexPrograms[representative]
			ctx.regexVerifiers[representative] = newNFAVerifierContext(program.program)
		}
		for groupIndex, group := range scanner.unanchoredGroups {
			if !scanner.unanchoredNeeded[groupIndex] {
				continue
			}
			representative := group[0]
			program := scanner.regexPrograms[representative]
			if program.boundedRepeat != nil {
				capacity := program.boundedRepeat.maximum - program.boundedRepeat.minimum + 1
				if capacity < 1 {
					capacity = 1
				}
				ctx.regexBounded[representative].matches = make([]fixedByteRegexMatch, 0, capacity)
			} else if !program.hasSimpleRepeat && !program.hasPrefixRepeat && program.fixedAnchor != nil {
				ctx.regexVerifiers[representative] = newNFAVerifierContext(program.program)
			} else if !program.hasSimpleRepeat && !program.hasPrefixRepeat && program.fixed != nil {
				ctx.regexFixed[representative].matches = make([]fixedByteRegexMatch, 0, len(program.fixed.sequences))
			} else if !program.hasSimpleRepeat && !program.hasPrefixRepeat {
				leftmost := scanner.expressions[program.expressionIndex].flags&CompileLeftmostStart != 0
				ctx.regexRunners[representative] = newNFASchedulerContext(program.program, leftmost)
			}
		}
	}
	return ctx
}

func prepareScanEvents(ctx *context, end int) {
	for ctx.pendingCount != 0 && ctx.pendingFirstEnd <= uint64(end) {
		event := ctx.popPendingEvent()
		if int(event.match.To) == end {
			ctx.readyEvents = append(ctx.readyEvents, event)
		}
	}
}

// pushPendingEvent 对自然有序的生产者使用 FIFO，并在首个乱序事件时退化为最小堆。锚定
// 正则验证器通常按扫描顺序发现终点，而定宽/NFA 执行器不一定如此。
func (ctx *context) pushPendingEvent(event scanEvent) {
	if ctx.pendingFIFO {
		if ctx.pendingCount == 0 {
			ctx.pendingEvents = ctx.pendingEvents[:0]
			ctx.pendingHead = 0
		}
		if ctx.pendingCount == 0 || !scanEventComesBefore(event, ctx.pendingEvents[len(ctx.pendingEvents)-1]) {
			ctx.pendingEvents = append(ctx.pendingEvents, event)
			ctx.pendingCount++
			if ctx.pendingCount == 1 {
				ctx.pendingFirstEnd = event.match.To
			}
			return
		}
		copy(ctx.pendingEvents, ctx.pendingEvents[ctx.pendingHead:])
		ctx.pendingEvents = ctx.pendingEvents[:len(ctx.pendingEvents)-ctx.pendingHead]
		ctx.pendingHead = 0
		ctx.pendingFIFO = false
	}
	ctx.pendingEvents = append(ctx.pendingEvents, event)
	ctx.pendingCount++
	for index := len(ctx.pendingEvents) - 1; index != 0; {
		parent := (index - 1) / 2
		if !scanEventComesBefore(ctx.pendingEvents[index], ctx.pendingEvents[parent]) {
			break
		}
		ctx.pendingEvents[index], ctx.pendingEvents[parent] = ctx.pendingEvents[parent], ctx.pendingEvents[index]
		index = parent
	}
	ctx.pendingFirstEnd = ctx.pendingEvents[0].match.To
}

func (ctx *context) popPendingEvent() scanEvent {
	if ctx.pendingFIFO {
		result := ctx.pendingEvents[ctx.pendingHead]
		ctx.pendingHead++
		ctx.pendingCount--
		if ctx.pendingCount == 0 {
			ctx.pendingEvents = ctx.pendingEvents[:0]
			ctx.pendingHead = 0
			ctx.pendingFirstEnd = 0
		} else {
			ctx.pendingFirstEnd = ctx.pendingEvents[ctx.pendingHead].match.To
		}
		return result
	}
	result := ctx.pendingEvents[0]
	lastIndex := len(ctx.pendingEvents) - 1
	last := ctx.pendingEvents[lastIndex]
	ctx.pendingEvents = ctx.pendingEvents[:lastIndex]
	ctx.pendingCount--
	if lastIndex == 0 {
		ctx.pendingFirstEnd = 0
		return result
	}
	ctx.pendingEvents[0] = last
	for index := 0; ; {
		left := index*2 + 1
		if left >= len(ctx.pendingEvents) {
			break
		}
		right := left + 1
		smallest := left
		if right < len(ctx.pendingEvents) && scanEventComesBefore(ctx.pendingEvents[right], ctx.pendingEvents[left]) {
			smallest = right
		}
		if !scanEventComesBefore(ctx.pendingEvents[smallest], ctx.pendingEvents[index]) {
			break
		}
		ctx.pendingEvents[index], ctx.pendingEvents[smallest] = ctx.pendingEvents[smallest], ctx.pendingEvents[index]
		index = smallest
	}
	ctx.pendingFirstEnd = ctx.pendingEvents[0].match.To
	return result
}

func (ctx *context) pendingEmpty() bool {
	return ctx.pendingCount == 0
}

func (ctx *context) pendingFirst() scanEvent {
	if ctx.pendingFIFO {
		return ctx.pendingEvents[ctx.pendingHead]
	}
	return ctx.pendingEvents[0]
}

func (ctx *context) pendingDue(end uint64) bool {
	return ctx.pendingCount != 0 && ctx.pendingFirstEnd <= end
}

func scanEventComesBefore(left, right scanEvent) bool {
	if left.match.To != right.match.To {
		return left.match.To < right.match.To
	}
	if left.expressionIndex != right.expressionIndex {
		return left.expressionIndex < right.expressionIndex
	}
	return left.match.From < right.match.From
}
