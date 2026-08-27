package scankit

// executionRoleGraph 是编译期规则关系的只读摘要。它描述候选如何激活验证器、验证器如何
// 向表达式消费者投递事件；扫描热路径不遍历该图。运行时继续使用 blockScanPlan 中已经
// 下沉的紧凑数组，避免为架构信息增加每字节间接访问。
type executionRoleGraph struct {
	roles  []executionRole
	edges  []executionRoleEdge
	budget executionPlanBudget
}

type executionRoleKind uint8

const (
	executionRoleLiteralCandidate executionRoleKind = iota + 1
	executionRoleAnchoredCandidate
	executionRoleFixedVerifier
	executionRoleFixedAnchorVerifier
	executionRolePrefixRepeatVerifier
	executionRoleAlwaysVerifier
	executionRoleUnicodeVerifier
	executionRoleAdvancedVerifier
	executionRoleReport
)

// executionRoleFallback 记录某个角色未能被更窄执行器承接的静态原因。它只服务编译期
// 审计；任何值都不会参与扫描期分派。
type executionRoleFallback uint8

const (
	executionRoleNoFallback executionRoleFallback = iota
	executionRoleNoLiteralFallback
	executionRoleUnboundedOffsetFallback
	executionRoleExactVerifierFallback
)

type executionRole struct {
	kind            executionRoleKind
	fallback        executionRoleFallback
	contextIndex    uint32
	triggerIndex    uint32
	expressionIndex uint32
}

type executionRoleEdgeKind uint8

const (
	executionRoleActivates executionRoleEdgeKind = iota + 1
	executionRoleBoundedDistance
	executionRoleDeduplicatesAtEnd
	executionRoleReports
	executionRoleMustDelay
)

type executionRoleEdge struct {
	from    uint32
	to      uint32
	minimum int32
	maximum int32
	kind    executionRoleEdgeKind
}

// executionPlanBudget 是编译计划的保守资源摘要。它不是运行时配额：资源上限仍由各执行器
// 在编译阶段分别强制；该摘要用于审计选择、比较计划变化并阻止新增路径忽略常驻成本。
type executionPlanBudget struct {
	roleCount          uint32
	edgeCount          uint32
	triggerCount       uint32
	verifierCount      uint32
	reportCount        uint32
	maxVerifierStates  uint32
	staticPlanBytes    uint32
	needsPendingEvents bool
}

func buildExecutionRoleGraph(plan blockScanPlan, triggers []scanTrigger, programs []compiledRegexProgram, expressions []compiledExpression, eventNeeded []bool, unicodeProperties []unicodePropertyProgram, unicodeApproximate []unicodeApproximateProgram, advancedEvents bool) executionRoleGraph {
	graph := executionRoleGraph{}
	reports := make([]uint32, len(expressions))
	for index := range reports {
		reports[index] = ^uint32(0)
		if !eventNeeded[index] {
			continue
		}
		reports[index] = graph.addRole(executionRole{kind: executionRoleReport, expressionIndex: uint32(index)})
		graph.budget.reportCount++
	}
	appendReports := func(from uint32, consumers []uint32, mustDelay bool) {
		for _, expressionIndex := range consumers {
			if expressionIndex >= uint32(len(reports)) || reports[expressionIndex] == ^uint32(0) {
				continue
			}
			graph.addEdge(from, reports[expressionIndex], executionRoleReports)
			// 所有生产者最终都按“同一表达式、同一结束位置”消解。显式保留这条边，
			// 使关系图能解释为何某些 lane 不能绕过统一事件层。
			graph.addEdge(from, reports[expressionIndex], executionRoleDeduplicatesAtEnd)
			if mustDelay || expressionRequiresDeferredDelivery(expressions[expressionIndex]) {
				graph.addEdge(from, reports[expressionIndex], executionRoleMustDelay)
			}
		}
	}
	for triggerIndex, trigger := range triggers {
		if int(triggerIndex) >= len(plan.triggers) {
			continue
		}
		lane := plan.triggers[triggerIndex]
		switch lane.kind {
		case blockTriggerLiteral:
			candidate := graph.addRole(executionRole{kind: executionRoleLiteralCandidate, triggerIndex: uint32(triggerIndex), expressionIndex: lane.literal.expressionIndex})
			appendReports(candidate, []uint32{lane.literal.expressionIndex}, false)
		case blockTriggerAnchored, blockTriggerAnchoredAtStart, blockTriggerInternalAnchored:
			candidate := graph.addRole(executionRole{kind: executionRoleAnchoredCandidate, triggerIndex: uint32(triggerIndex), contextIndex: lane.anchored.contextIndex})
			verifier := graph.addRole(executionRole{kind: executionRoleAlwaysVerifier, contextIndex: lane.anchored.contextIndex, fallback: executionRoleExactVerifierFallback})
			graph.addEdge(candidate, verifier, executionRoleActivates)
			maximum := int32(lane.anchored.anchorMaxOffset)
			if lane.anchored.internalAnchor {
				maximum = -1
			}
			graph.addBoundedDistanceEdge(candidate, verifier, int32(lane.anchored.anchorMinOffset), maximum)
			appendReports(verifier, lane.anchored.consumers, true)
		}
		_ = trigger
	}
	seenFixed := make(map[uint32]uint32)
	seenFixedAnchor := make(map[uint32]uint32)
	seenPrefixRepeat := make(map[uint32]uint32)
	seenBoundedRepeat := make(map[uint32]uint32)
	seenAlternation := make(map[uint32]uint32)
	for value := range plan.unanchored.fixed {
		for _, lane := range plan.unanchored.fixed[value] {
			if _, ok := seenFixed[lane.contextIndex]; ok {
				continue
			}
			role := graph.addRole(executionRole{kind: executionRoleFixedVerifier, contextIndex: lane.contextIndex})
			seenFixed[lane.contextIndex] = role
			appendReports(role, lane.consumers, true)
		}
		for _, lane := range plan.unanchored.fixedAnchor[value] {
			if _, ok := seenFixedAnchor[lane.contextIndex]; ok {
				continue
			}
			role := graph.addRole(executionRole{kind: executionRoleFixedAnchorVerifier, contextIndex: lane.contextIndex})
			seenFixedAnchor[lane.contextIndex] = role
			appendReports(role, lane.consumers, true)
		}
		for _, lane := range plan.unanchored.prefixRepeat[value] {
			if _, ok := seenPrefixRepeat[lane.contextIndex]; ok {
				continue
			}
			role := graph.addRole(executionRole{kind: executionRolePrefixRepeatVerifier, contextIndex: lane.contextIndex})
			seenPrefixRepeat[lane.contextIndex] = role
			appendReports(role, lane.consumers, true)
		}
		for _, lane := range plan.unanchored.boundedRepeat[value] {
			if _, ok := seenBoundedRepeat[lane.contextIndex]; ok {
				continue
			}
			role := graph.addRole(executionRole{kind: executionRoleFixedVerifier, contextIndex: lane.contextIndex})
			seenBoundedRepeat[lane.contextIndex] = role
			appendReports(role, lane.consumers, true)
		}
		for _, lane := range plan.unanchored.alternation[value] {
			if _, ok := seenAlternation[lane.contextIndex]; ok {
				continue
			}
			role := graph.addRole(executionRole{kind: executionRoleFixedAnchorVerifier, contextIndex: lane.contextIndex})
			seenAlternation[lane.contextIndex] = role
			appendReports(role, lane.consumers, true)
		}
	}
	for _, lane := range plan.unanchored.always {
		role := graph.addRole(executionRole{kind: executionRoleAlwaysVerifier, contextIndex: lane.contextIndex, fallback: executionRoleExactVerifierFallback})
		appendReports(role, lane.consumers, true)
	}
	for _, program := range unicodeProperties {
		role := graph.addRole(executionRole{kind: executionRoleUnicodeVerifier, expressionIndex: program.expressionIndex})
		appendReports(role, []uint32{program.expressionIndex}, true)
	}
	for _, program := range unicodeApproximate {
		role := graph.addRole(executionRole{kind: executionRoleUnicodeVerifier, expressionIndex: program.expressionIndex})
		appendReports(role, []uint32{program.expressionIndex}, true)
	}
	if advancedEvents {
		for index := range expressions {
			if !eventNeeded[index] {
				continue
			}
			role := graph.addRole(executionRole{kind: executionRoleAdvancedVerifier, expressionIndex: uint32(index)})
			appendReports(role, []uint32{uint32(index)}, true)
		}
	}
	for _, program := range programs {
		if states := len(program.program.states); states > int(graph.budget.maxVerifierStates) {
			graph.budget.maxVerifierStates = uint32(states)
		}
	}
	graph.budget.roleCount = uint32(len(graph.roles))
	graph.budget.edgeCount = uint32(len(graph.edges))
	graph.budget.triggerCount = uint32(len(triggers))
	graph.budget.verifierCount = graph.countVerifiers()
	graph.budget.staticPlanBytes = uint32(len(graph.roles))*uint32(executionRoleSize) + uint32(len(graph.edges))*uint32(executionRoleEdgeSize)
	graph.budget.needsPendingEvents = !planHasNaturallyDirectEvents(plan, expressions, eventNeeded, advancedEvents)
	return graph
}

const (
	executionRoleSize     = 16
	executionRoleEdgeSize = 24
)

func (graph *executionRoleGraph) addRole(role executionRole) uint32 {
	index := uint32(len(graph.roles))
	graph.roles = append(graph.roles, role)
	return index
}

func (graph *executionRoleGraph) addEdge(from, to uint32, kind executionRoleEdgeKind) {
	graph.edges = append(graph.edges, executionRoleEdge{from: from, to: to, kind: kind})
}

func (graph *executionRoleGraph) addBoundedDistanceEdge(from, to uint32, minimum, maximum int32) {
	graph.edges = append(graph.edges, executionRoleEdge{from: from, to: to, minimum: minimum, maximum: maximum, kind: executionRoleBoundedDistance})
}

func expressionRequiresDeferredDelivery(expression compiledExpression) bool {
	return expression.flags&(CompileQuiet|CompileSingleMatch|CompileLeftmostStart|CompileCombination) != 0 ||
		expression.constraint.hasMinOffset || expression.constraint.hasMaxOffset || expression.constraint.hasMinLength
}

func (graph executionRoleGraph) countVerifiers() uint32 {
	var count uint32
	for _, role := range graph.roles {
		switch role.kind {
		case executionRoleFixedVerifier, executionRoleFixedAnchorVerifier, executionRolePrefixRepeatVerifier, executionRoleAlwaysVerifier, executionRoleUnicodeVerifier, executionRoleAdvancedVerifier:
			count++
		}
	}
	return count
}

func planHasNaturallyDirectEvents(plan blockScanPlan, expressions []compiledExpression, eventNeeded []bool, advancedEvents bool) bool {
	if advancedEvents || len(plan.triggers) != 0 || len(plan.unanchored.always) != 0 || len(plan.unanchored.fixedAnchor) != 0 || len(plan.unanchored.prefixRepeat) != 0 {
		return false
	}
	for index, expression := range expressions {
		if !eventNeeded[index] {
			continue
		}
		if expressionRequiresDeferredDelivery(expression) {
			return false
		}
	}
	return true
}

func directEventDeliveryEligible(expressions []compiledExpression, eventNeeded []bool, combinations []combinationProgram) bool {
	if len(combinations) != 0 {
		return false
	}
	for index, expression := range expressions {
		// 未参与组合的 QUIET 表达式在编译阶段已不进入任何事件生产通道，不能阻止
		// 其他可观察表达式使用直达投递。
		if !eventNeeded[index] {
			continue
		}
		if expressionRequiresDeferredDelivery(expression) {
			return false
		}
	}
	return true
}

func (graph executionRoleGraph) valid(expressions []compiledExpression, eventNeeded []bool) bool {
	if graph.budget.roleCount != uint32(len(graph.roles)) || graph.budget.edgeCount != uint32(len(graph.edges)) {
		return false
	}
	reported := make([]bool, len(expressions))
	for _, edge := range graph.edges {
		if edge.from >= uint32(len(graph.roles)) || edge.to >= uint32(len(graph.roles)) || edge.kind == 0 {
			return false
		}
		if graph.roles[edge.to].kind == executionRoleReport {
			reported[graph.roles[edge.to].expressionIndex] = true
		}
	}
	for index := range expressions {
		if eventNeeded[index] && !reported[index] && graph.budget.verifierCount != 0 {
			return false
		}
	}
	return true
}
