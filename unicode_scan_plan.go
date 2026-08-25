package scankit

// buildBlockUnicodePlan 分类 UCP 遍历必须附带的字节工作。它消费不可变块扫描计划，避免在
// 扫描时重新判断源分组；UCP 程序本身仍保持 rune 感知。
func buildBlockUnicodePlan(plan blockScanPlan, advancedEvents bool) blockUnicodePlan {
	result := blockUnicodePlan{scanPlan: unicodeScanPlanGeneric}
	if advancedEvents {
		return result
	}

	hasUnanchored := plan.unanchored.hasLanes()
	simpleRepeatsOnly := true
	for _, lanes := range plan.unanchored.fixed {
		if len(lanes) != 0 {
			simpleRepeatsOnly = false
			break
		}
	}
	if simpleRepeatsOnly {
		for _, lanes := range plan.unanchored.fixedAnchor {
			if len(lanes) != 0 {
				simpleRepeatsOnly = false
				break
			}
		}
	}
	if simpleRepeatsOnly {
		for _, lane := range plan.unanchored.always {
			if !lane.hasSimpleRepeat {
				simpleRepeatsOnly = false
				break
			}
			result.simpleRepeats = append(result.simpleRepeats, lane)
		}
	}

	hasActiveTrigger := false
	literalsOnly := true
	for _, lane := range plan.triggers {
		if lane.kind == blockTriggerInactive {
			continue
		}
		hasActiveTrigger = true
		if lane.kind != blockTriggerLiteral {
			literalsOnly = false
			break
		}
	}
	if !hasActiveTrigger {
		if !hasUnanchored {
			result.scanPlan = unicodeScanPlanPure
			return result
		}
		if simpleRepeatsOnly {
			result.scanPlan = unicodeScanPlanSimpleRepeats
			return result
		}
		return result
	}
	if !hasUnanchored && literalsOnly {
		result.scanPlan = unicodeScanPlanLiteralAC
	}
	return result
}
