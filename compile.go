package scankit

import (
	"errors"
	"fmt"
	"sync"
	"unicode/utf8"
)

// Compile 根据全部表达式构建一个不可变 Scanner。它接受字面量模式和在固定或有界位置
// 包含必需字面量锚点的正则表达式；其他语法有效的正则会返回 ErrUnsupportedExpression，
// 而不会静默退化为逐规则扫描。
func Compile(expressions []Expression) (*Scanner, error) {
	if len(expressions) == 0 {
		return nil, ErrEmptyExpressions
	}

	seen := make(map[uint32]struct{}, len(expressions))
	indexes := make(map[uint32]uint32, len(expressions))
	builder := newLiteralBuilder()
	compiled := make([]compiledExpression, len(expressions))
	triggers := make([]scanTrigger, 0, len(expressions))
	regexPrograms := make([]compiledRegexProgram, 0)
	unicodeProperties := make([]unicodePropertyProgram, 0)
	unicodeApproximate := make([]unicodeApproximateProgram, 0)
	unicodeAlternation := false
	anchoredGroups := make([][]uint32, 0)
	anchoredGroupIndex := make(map[anchoredGroupKey]int)
	byteRegexPlans := make(map[byteRegexPlanCacheKey]byteRegexCompilePlan)
	unanchoredRegex := make([]uint32, 0)
	unanchoredGroups := make([][]uint32, 0)
	unanchoredGroupIndex := make(map[unanchoredGroupKey]int)
	emptyRegex := make([]uint32, 0)
	hammingLiterals := make([]hammingLiteral, 0)
	hammingRegexes := make([]hammingFixedRegex, 0)
	hammingNFAs := make([]hammingNFA, 0)
	editLiterals := make([]editLiteral, 0)
	editRegexes := make([]editFixedRegex, 0)
	editClassRepeats := make([]editClassRepeat, 0)
	editNFAs := make([]editNFA, 0)
	maxEditLength := 0
	requiresUTF8 := false
	combinations := make([]combinationProgram, 0)
	eventCapacity := len(expressions)
	for index, expression := range expressions {
		if _, ok := seen[expression.Id]; ok {
			return nil, fmt.Errorf("%w: %d", ErrDuplicateExpression, expression.Id)
		}
		seen[expression.Id] = struct{}{}
		indexes[expression.Id] = uint32(index)
		if expression.Flags&^(CompileCaseless|CompileDotAll|CompileMultiline|CompileSingleMatch|CompileAllowEmpty|CompileUTF8|CompileUnicodeProperties|CompilePrefilter|CompileLeftmostStart|CompileCombination|CompileQuiet) != 0 {
			return nil, fmt.Errorf("%w: expression %d has flags %#x", ErrUnsupportedFlag, expression.Id, expression.Flags)
		}
		if expression.Flags&CompileUnicodeProperties != 0 && expression.Flags&CompileUTF8 == 0 {
			return nil, fmt.Errorf("%w: expression %d UCP requires UTF8", ErrUnsupportedFlag, expression.Id)
		}
		unicodePropertyCandidate := expression.Flags&(CompileUTF8|CompileUnicodeProperties) == CompileUTF8|CompileUnicodeProperties && (containsRegexMeta(expression.Pattern) || !isASCIIPattern(expression.Pattern) || expression.Ext != nil && expression.Ext.Flags&(ExtEditDistance|ExtHammingDistance) != 0)
		if expression.Flags&CompileUTF8 != 0 {
			if !utf8.ValidString(expression.Pattern) || !unicodePropertyCandidate && containsRegexMeta(expression.Pattern) || expression.Flags&CompileCaseless != 0 && !isASCIIPattern(expression.Pattern) && !unicodePropertyCandidate {
				return nil, fmt.Errorf("%w: expression %d UTF-8/UCP execution currently requires a valid plain literal; CASELESS is limited to ASCII", ErrUnsupportedFlag, expression.Id)
			}
			requiresUTF8 = true
		}
		if expression.Pattern == "" && expression.Flags&CompileAllowEmpty == 0 {
			return nil, fmt.Errorf("%w: expression %d", ErrInvalidExpression, expression.Id)
		}
		constraint, err := compileMatchConstraint(expression.Ext)
		if err != nil {
			return nil, fmt.Errorf("%w: expression %d", err, expression.Id)
		}
		compiled[index] = compiledExpression{id: expression.Id, length: uint32(len(expression.Pattern)), flags: expression.Flags, constraint: constraint}
		if expression.Flags&CompileCombination != 0 {
			if expression.Ext != nil && expression.Ext.Flags&(ExtEditDistance|ExtHammingDistance) != 0 {
				return nil, fmt.Errorf("%w: expression %d approximate matching is not valid for combinations", ErrUnsupportedExtension, expression.Id)
			}
			if expression.Flags&^(CompileCombination|CompileSingleMatch|CompileQuiet|CompileLeftmostStart|CompilePrefilter) != 0 {
				return nil, fmt.Errorf("%w: expression %d combination flags %#x", ErrUnsupportedFlag, expression.Id, expression.Flags)
			}
			root, err := parseCombination(expression.Pattern)
			if err != nil {
				return nil, fmt.Errorf("%w: expression %d: %v", ErrInvalidCombination, expression.Id, err)
			}
			combinations = append(combinations, combinationProgram{expressionIndex: uint32(index), root: root})
			continue
		}
		if unicodePropertyCandidate {
			programs, err := compileUnicodePropertyPrograms(uint32(index), expression.Pattern, expression.Flags)
			if err != nil {
				kind := ErrUnsupportedExpression
				if errors.Is(err, ErrRegexTooComplex) {
					kind = ErrRegexTooComplex
				}
				return nil, fmt.Errorf("%w: expression %d: %v", kind, expression.Id, err)
			}
			for programIndex := range programs {
				program := &programs[programIndex]
				if expression.Flags&CompileCaseless != 0 && !program.flagsApplied {
					program.enableCaseless()
				}
				program.prepareASCII()
				if program.nullable && expression.Flags&CompileAllowEmpty == 0 {
					return nil, fmt.Errorf("%w: expression %d can match an empty code-point sequence", ErrUnsupportedExpression, expression.Id)
				}
			}
			if expression.Ext != nil && expression.Ext.Flags&(ExtEditDistance|ExtHammingDistance) != 0 {
				distance := expression.Ext.EditDistance
				hamming := false
				if expression.Ext.Flags&ExtHammingDistance != 0 {
					distance = expression.Ext.HammingDistance
					hamming = true
				}
				if distance > maxEditLiteralDistance {
					return nil, fmt.Errorf("%w: expression %d UCP approximate distance exceeds %d", ErrUnsupportedExtension, expression.Id, maxEditLiteralDistance)
				}
				for _, program := range programs {
					atoms, ok := unicodePropertyFixedAtoms(program)
					if ok {
						unicodeApproximate = append(unicodeApproximate, unicodeApproximateProgram{
							expressionIndex: uint32(index),
							atoms:           atoms,
							distance:        distance,
							hamming:         hamming,
						})
						continue
					}
					minimumWidth, maximumWidth, graphOK := unicodePropertyGraphWidthRange(program.graph)
					if !graphOK || program.hasAssertions || len(program.graph.states) > maxCachedClosureStates || (hamming && minimumWidth != maximumWidth) {
						return nil, fmt.Errorf("%w: expression %d UCP approximate matching requires a bounded non-empty rune graph without assertions; Hamming distance also requires fixed width", ErrUnsupportedExtension, expression.Id)
					}
					unicodeApproximate = append(unicodeApproximate, unicodeApproximateProgram{
						expressionIndex: uint32(index),
						graph:           program.graph,
						minimumWidth:    minimumWidth,
						maximumWidth:    maximumWidth,
						distance:        distance,
						hamming:         hamming,
					})
				}
				unicodeAlternation = unicodeAlternation || len(programs) > 1
				continue
			}
			unicodeProperties = append(unicodeProperties, programs...)
			unicodeAlternation = unicodeAlternation || len(programs) > 1
			for _, program := range programs {
				unicodeAlternation = unicodeAlternation || program.hasAlternation
			}
			continue
		}
		if expression.Ext != nil && expression.Ext.Flags&ExtEditDistance != 0 {
			if expression.Pattern == "" || expression.Flags&(CompileCaseless|CompileUTF8) != 0 || expression.Ext.EditDistance > maxEditLiteralDistance {
				return nil, fmt.Errorf("%w: expression %d edit distance requires a non-empty byte expression without CASELESS or UTF8 and distance at most %d", ErrUnsupportedExtension, expression.Id, maxEditLiteralDistance)
			}
			if containsRegexMeta(expression.Pattern) {
				root, parseErr := parseRegexWithFlags(expression.Pattern, expression.Flags)
				if parseErr != nil {
					return nil, parseErr
				}
				applyRegexFlags(root, 0)
				fixed, ok := extractFixedByteRegex(root)
				if ok {
					for _, sequence := range fixed.sequences {
						if len(sequence.classes) > maxEditLiteralLength {
							return nil, fmt.Errorf("%w: expression %d edit distance width exceeds %d", ErrUnsupportedExtension, expression.Id, maxEditLiteralLength)
						}
						if len(sequence.classes) > maxEditLength {
							maxEditLength = len(sequence.classes)
						}
					}
					editRegexes = append(editRegexes, editFixedRegex{expressionIndex: uint32(index), sequences: fixed.sequences, distance: expression.Ext.EditDistance})
					continue
				}
				if class, minimum, maximum, ok := extractBoundedByteClassRepeat(root); ok {
					editClassRepeats = append(editClassRepeats, editClassRepeat{
						expressionIndex: uint32(index),
						class:           class,
						minimum:         minimum,
						maximum:         maximum,
						distance:        expression.Ext.EditDistance,
					})
					continue
				}
				program, minimumWidth, maximumWidth, supported, compileErr := compileEditNFA(root, expression.Ext.EditDistance)
				if compileErr != nil {
					return nil, compileErr
				}
				if !supported {
					return nil, fmt.Errorf("%w: expression %d edit distance requires a bounded non-empty byte regex", ErrUnsupportedExtension, expression.Id)
				}
				editNFAs = append(editNFAs, editNFA{expressionIndex: uint32(index), program: program, minimumWidth: minimumWidth, maximumWidth: maximumWidth, distance: expression.Ext.EditDistance})
				continue
			}
			if len(expression.Pattern) > maxEditLiteralLength {
				return nil, fmt.Errorf("%w: expression %d edit distance literal exceeds %d bytes", ErrUnsupportedExtension, expression.Id, maxEditLiteralLength)
			}
			editLiterals = append(editLiterals, editLiteral{
				expressionIndex: uint32(index),
				pattern:         expression.Pattern,
				distance:        expression.Ext.EditDistance,
			})
			if len(expression.Pattern) > maxEditLength {
				maxEditLength = len(expression.Pattern)
			}
			continue
		}
		if expression.Ext != nil && expression.Ext.Flags&ExtHammingDistance != 0 {
			if expression.Pattern == "" || expression.Flags&(CompileCaseless|CompileUTF8) != 0 {
				return nil, fmt.Errorf("%w: expression %d Hamming distance requires a non-empty byte expression without CASELESS or UTF8", ErrUnsupportedExtension, expression.Id)
			}
			if containsRegexMeta(expression.Pattern) {
				root, parseErr := parseRegexWithFlags(expression.Pattern, expression.Flags)
				if parseErr != nil {
					return nil, parseErr
				}
				applyRegexFlags(root, 0)
				fixed, ok := extractFixedByteRegex(root)
				if ok && fixedByteRegexHasSingleWidth(fixed) {
					hammingRegexes = append(hammingRegexes, hammingFixedRegex{expressionIndex: uint32(index), sequences: fixed.sequences, distance: expression.Ext.HammingDistance})
					continue
				}
				program, width, supported, compileErr := compileHammingNFA(root, expression.Ext.HammingDistance)
				if compileErr != nil {
					return nil, compileErr
				}
				if !supported {
					return nil, fmt.Errorf("%w: expression %d Hamming distance requires a bounded fixed-width byte regex", ErrUnsupportedExtension, expression.Id)
				}
				hammingNFAs = append(hammingNFAs, hammingNFA{expressionIndex: uint32(index), program: program, width: width, distance: expression.Ext.HammingDistance})
				continue
			}
			hammingLiterals = append(hammingLiterals, hammingLiteral{
				expressionIndex: uint32(index),
				pattern:         expression.Pattern,
				distance:        expression.Ext.HammingDistance,
			})
			continue
		}
		if containsRegexMeta(expression.Pattern) || expression.Flags&CompileCaseless != 0 || expression.Pattern == "" {
			planKey := byteRegexPlanCacheKey{pattern: expression.Pattern, flags: expression.Flags & byteRegexPlanLanguageFlags}
			plan, ok := byteRegexPlans[planKey]
			if !ok {
				var root *regexNode
				if expression.Pattern == "" {
					root = &regexNode{kind: regexEmpty}
				} else {
					root, err = parseRegexWithFlags(expression.Pattern, expression.Flags)
				}
				if err != nil {
					return nil, err
				}
				applyRegexFlags(root, 0)
				plan, err = compileByteRegexPlan(root, expression.Flags)
				if err != nil {
					return nil, err
				}
				byteRegexPlans[planKey] = plan
			}
			if plan.analysis.min == 0 && expression.Flags&CompileAllowEmpty == 0 {
				return nil, fmt.Errorf("%w: expression %d can match an empty byte sequence", ErrUnsupportedExpression, expression.Id)
			}
			regexIndex := uint32(len(regexPrograms))
			compiledProgram := compiledRegexProgram{
				expressionIndex: uint32(index),
				maxLength:       plan.analysis.max,
				program:         plan.program,
				simpleRepeat:    plan.simpleRepeat,
				hasSimpleRepeat: plan.hasSimpleRepeat,
				fixed:           plan.fixed,
				fixedAnchor:     plan.fixedAnchor,
			}
			if (!plan.hasBoundedAnchor && !plan.hasInternalAnchor) || plan.analysis.min == 0 {
				compiledProgram.unanchored = true
				regexPrograms = append(regexPrograms, compiledProgram)
				unanchoredRegex = append(unanchoredRegex, regexIndex)
				key := unanchoredGroupKey{
					program:  nfaProgramFingerprint(compiledProgram.program),
					leftmost: expression.Flags&CompileLeftmostStart != 0,
				}
				if groupIndex, ok := unanchoredGroupIndex[key]; ok {
					unanchoredGroups[groupIndex] = append(unanchoredGroups[groupIndex], regexIndex)
				} else {
					unanchoredGroupIndex[key] = len(unanchoredGroups)
					unanchoredGroups = append(unanchoredGroups, []uint32{regexIndex})
				}
				if plan.analysis.min == 0 {
					emptyRegex = append(emptyRegex, regexIndex)
				}
				continue
			}
			selectedAnchor := plan.anchor
			if plan.hasInternalAnchor {
				selectedAnchor = plan.internalAnchor
				compiledProgram.internalAnchor = true
				compiledProgram.internalPrefixClass = plan.internalPrefixClass
				compiledProgram.internalLeading = plan.internalLeading
			}
			compiledProgram.anchorMinOffset = uint32(selectedAnchor.minOffset)
			if selectedAnchor.maxOffset != unboundedRepeat {
				compiledProgram.anchorMaxOffset = uint32(selectedAnchor.maxOffset)
			}
			compiledProgram.anchorLength = uint32(len(selectedAnchor.literal))
			compiledProgram.anchorByte = selectedAnchor.literal[0]
			compiledProgram.prefixDFAStates = plan.prefixDFAStates
			compiledProgram.leftmostOnly = plan.leftmostOnly
			compiledProgram.prefixClass = plan.prefixClass
			compiledProgram.hasPrefixClass = plan.hasPrefixClass
			compiledProgram.suffixClass = plan.suffixClass
			compiledProgram.hasSuffixClass = plan.hasSuffixClass
			compiledProgram.suffixChecks = plan.suffixChecks
			regexPrograms = append(regexPrograms, compiledProgram)
			candidateCount := 0
			if !plan.hasInternalAnchor {
				candidateCount = selectedAnchor.maxOffset - selectedAnchor.minOffset + 1
			}
			// PII 规则常有较小的有界浮动锚点窗口（例如邮箱本地部分）。预留该空间可使
			// 首次扫描无分配，同时避免为宽度合法但很大的正则分配过大的 ctx。
			if candidateCount <= 128 {
				eventCapacity += candidateCount
			}
			key := anchoredGroupKey{
				program:             nfaProgramFingerprint(compiledProgram.program),
				anchorMinOffset:     compiledProgram.anchorMinOffset,
				anchorMaxOffset:     compiledProgram.anchorMaxOffset,
				anchorLength:        compiledProgram.anchorLength,
				maxLength:           compiledProgram.maxLength,
				internal:            compiledProgram.internalAnchor,
				internalPrefixClass: compiledProgram.internalPrefixClass,
				internalLeading:     compiledProgram.internalLeading,
				prefixClass:         compiledProgram.prefixClass,
				hasPrefixClass:      compiledProgram.hasPrefixClass,
				suffixClass:         compiledProgram.suffixClass,
				hasSuffixClass:      compiledProgram.hasSuffixClass,
				hasSuffixChecks:     compiledProgram.suffixChecks != nil,
				leftmost:            compiledProgram.leftmostOnly,
			}
			if compiledProgram.suffixChecks != nil {
				key.suffixChecks = *compiledProgram.suffixChecks
			}
			if groupIndex, ok := anchoredGroupIndex[key]; ok {
				anchoredGroups[groupIndex] = append(anchoredGroups[groupIndex], regexIndex)
			} else {
				groupIndex := len(anchoredGroups)
				anchoredGroupIndex[key] = groupIndex
				anchoredGroups = append(anchoredGroups, []uint32{regexIndex})
				builder.add([]byte(selectedAnchor.literal), uint32(len(triggers)))
				triggers = append(triggers, scanTrigger{
					kind:            scanRegex,
					expressionIndex: uint32(index),
					regexIndex:      regexIndex,
					regexGroupIndex: uint32(groupIndex),
				})
			}
			continue
		}
		builder.add([]byte(expression.Pattern), uint32(len(triggers)))
		triggers = append(triggers, scanTrigger{kind: scanLiteral, expressionIndex: uint32(index)})
	}

	for combinationIndex := range combinations {
		combination := &combinations[combinationIndex]
		if err := resolveCombinationOperands(combination.root, combination.expressionIndex, indexes, compiled); err != nil {
			return nil, err
		}
	}
	eventNeeded := make([]bool, len(compiled))
	for index, expression := range compiled {
		eventNeeded[index] = expression.flags&CompileQuiet == 0
	}
	for _, combination := range combinations {
		markCombinationOperands(combination.root, eventNeeded)
	}
	anchoredNeeded := make([]bool, len(anchoredGroups))
	for groupIndex, group := range anchoredGroups {
		for _, regexIndex := range group {
			if eventNeeded[regexPrograms[regexIndex].expressionIndex] {
				anchoredNeeded[groupIndex] = true
				break
			}
		}
	}
	unanchoredNeeded := make([]bool, len(unanchoredGroups))
	for groupIndex, group := range unanchoredGroups {
		for _, regexIndex := range group {
			if eventNeeded[regexPrograms[regexIndex].expressionIndex] {
				unanchoredNeeded[groupIndex] = true
				break
			}
		}
	}
	advancedEvents := false
	for _, regexIndex := range emptyRegex {
		if eventNeeded[regexPrograms[regexIndex].expressionIndex] {
			advancedEvents = true
			break
		}
	}
	singleByteOnly := len(triggers) != 0
	var singleByteValues [2]byte
	singleByteValueCount := 0
	var singleByteTriggers [256][]scanTrigger
	if singleByteOnly {
		for _, trigger := range triggers {
			var value byte
			if trigger.kind == scanLiteral {
				expression := expressions[trigger.expressionIndex]
				if len(expression.Pattern) != 1 {
					singleByteOnly = false
					break
				}
				value = expression.Pattern[0]
			} else {
				regex := regexPrograms[trigger.regexIndex]
				if regex.internalAnchor {
					singleByteOnly = false
					break
				}
				if regex.anchorLength != 1 {
					singleByteOnly = false
					break
				}
				value = regex.anchorByte
			}
			singleByteTriggers[value] = append(singleByteTriggers[value], trigger)
			if singleByteValueCount == 0 {
				singleByteValues[0] = value
				singleByteValueCount = 1
			} else if singleByteValueCount == 1 && value != singleByteValues[0] {
				singleByteValues[1] = value
				singleByteValueCount = 2
			} else if singleByteValueCount == 2 && value != singleByteValues[0] && value != singleByteValues[1] {
				singleByteValueCount = 3
			}
		}
	}
	if singleByteValueCount == 1 {
		// 字匹配器会比较两个值。复制唯一值而非让第二个值为 NUL，避免单规则数据库中
		// 每个零字节都成为无效候选。
		singleByteValues[1] = singleByteValues[0]
	}
	singleByteFast := singleByteOnly && singleByteValueCount <= len(singleByteValues)
	singleByteSimple := singleByteOnly
	var singleByteRegex [256]uint32
	if singleByteSimple {
		for _, trigger := range triggers {
			if trigger.kind != scanRegex {
				singleByteSimple = false
				break
			}
			regex := regexPrograms[trigger.regexIndex]
			groupIndex := int(trigger.regexGroupIndex)
			if len(singleByteTriggers[regex.anchorByte]) != 1 || len(anchoredGroups[groupIndex]) != 1 || !anchoredNeeded[groupIndex] || !eventNeeded[regex.expressionIndex] {
				singleByteSimple = false
				break
			}
			singleByteRegex[regex.anchorByte] = trigger.regexIndex
		}
	}
	if !advancedEvents {
		for _, literal := range hammingLiterals {
			if eventNeeded[literal.expressionIndex] {
				advancedEvents = true
				break
			}
		}
	}
	if !advancedEvents {
		for _, regex := range hammingRegexes {
			if eventNeeded[regex.expressionIndex] {
				advancedEvents = true
				break
			}
		}
	}
	if !advancedEvents {
		for _, regex := range hammingNFAs {
			if eventNeeded[regex.expressionIndex] {
				advancedEvents = true
				break
			}
		}
	}
	if !advancedEvents {
		for _, literal := range editLiterals {
			if eventNeeded[literal.expressionIndex] {
				advancedEvents = true
				break
			}
		}
	}
	if !advancedEvents {
		for _, regex := range editRegexes {
			if eventNeeded[regex.expressionIndex] {
				advancedEvents = true
				break
			}
		}
	}
	if !advancedEvents {
		for _, repeat := range editClassRepeats {
			if eventNeeded[repeat.expressionIndex] {
				advancedEvents = true
				break
			}
		}
	}
	if !advancedEvents {
		for _, regex := range editNFAs {
			if eventNeeded[regex.expressionIndex] {
				advancedEvents = true
				break
			}
		}
	}
	blockScanPlan := buildBlockScanPlan(regexPrograms, compiled, unanchoredGroups, unanchoredNeeded, triggers, anchoredGroups, anchoredNeeded, eventNeeded, advancedEvents)
	automaton := builder.freeze()
	directLiterals := false
	for state := range automaton.outputStart {
		if automaton.outputEnd[state]-automaton.outputStart[state] > 1 {
			directLiterals = len(regexPrograms) == 0 && len(unicodeProperties) == 0 && len(unicodeApproximate) == 0 && len(combinations) == 0 && !advancedEvents && len(triggers) == len(compiled)
			break
		}
	}
	if directLiterals {
		for _, expression := range compiled {
			if expression.flags&(CompileSingleMatch|CompileQuiet) != 0 || expression.constraint.hasMinOffset || expression.constraint.hasMaxOffset || expression.constraint.hasMinLength {
				directLiterals = false
				break
			}
		}
	}
	directSingleEvent := len(combinations) == 0
	if directSingleEvent {
		for _, expression := range compiled {
			if expression.flags&(CompileSingleMatch|CompileQuiet) != 0 || expression.constraint.hasMinOffset || expression.constraint.hasMaxOffset || expression.constraint.hasMinLength {
				directSingleEvent = false
				break
			}
		}
	}
	fixedOnlyBlock := !advancedEvents && len(triggers) == 0 && len(blockScanPlan.unanchored.always) == 0 && blockScanPlan.unanchored.hasLanes()
	unicodePlan := blockScanPlan.unicode.scanPlan
	// 单根字特化仅对选择性强的固定前导锚点有效。浮动锚点和单字节前缀的候选工作量较多，
	// 通用扫描器更适合。
	singleRootFixedAnchor := hasSingleRootFixedAnchoredTrigger(automaton, blockScanPlan)
	// 大型等价锚定表达式组（例如多个租户共用一条规则体）会让每个触发器生成多个未来事件。
	// 仅这些分组启用 FIFO 快路径；普通数据库保留紧凑的堆路径，避免增加每事件分支。
	orderedPendingEvents := false
	for _, group := range anchoredGroups {
		if len(group) >= 8 {
			orderedPendingEvents = true
			break
		}
	}

	return &Scanner{
		expressions:           compiled,
		automaton:             automaton,
		triggers:              triggers,
		regexPrograms:         regexPrograms,
		unicodeProperties:     unicodeProperties,
		unicodeApproximate:    unicodeApproximate,
		unicodeAlternation:    unicodeAlternation,
		unicodeScanPlan:       unicodePlan,
		anchoredGroups:        anchoredGroups,
		anchoredNeeded:        anchoredNeeded,
		unanchoredRegex:       unanchoredRegex,
		unanchoredGroups:      unanchoredGroups,
		unanchoredNeeded:      unanchoredNeeded,
		blockScanPlan:         blockScanPlan,
		eventNeeded:           eventNeeded,
		emptyRegex:            emptyRegex,
		hammingLiterals:       hammingLiterals,
		hammingRegexes:        hammingRegexes,
		hammingNFAs:           hammingNFAs,
		editLiterals:          editLiterals,
		editRegexes:           editRegexes,
		editClassRepeats:      editClassRepeats,
		editNFAs:              editNFAs,
		maxEditLength:         maxEditLength,
		advancedEvents:        advancedEvents,
		directLiterals:        directLiterals,
		directSingleEvent:     directSingleEvent,
		fixedOnlyBlock:        fixedOnlyBlock,
		requiresUTF8:          requiresUTF8,
		singleByteOnly:        singleByteOnly,
		singleByteFast:        singleByteFast,
		singleByteSimple:      singleByteSimple,
		singleRootFixedAnchor: singleRootFixedAnchor,
		orderedPendingEvents:  orderedPendingEvents,
		singleByteValues:      singleByteValues,
		singleByteTriggers:    singleByteTriggers,
		singleByteRegex:       singleByteRegex,
		combinations:          combinations,
		eventCapacity:         eventCapacity,
		contextPool:           &sync.Pool{},
	}, nil
}
