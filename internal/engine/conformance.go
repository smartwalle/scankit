package engine

import (
	"fmt"
	"reflect"

	"github.com/smartwalle/scankit/internal/nfagraph"
)

// ConformanceInput 描述后端一致性验证输入。
type ConformanceInput struct {
	Name string
	Data []byte
}

// CompareBackends 对同一图的 NFA/DFA 结果逐样例比对。
func CompareBackends(g *nfagraph.Graph, inputs []ConformanceInput) error {
	nfaProgram, err := Build(g, KindNFA)
	if err != nil {
		return err
	}
	dfaProgram, err := Build(g, KindDFA)
	if err != nil {
		return err
	}
	for index, input := range inputs {
		if !nfaProgram.EquivalentResults(dfaProgram, input.Data) {
			return fmt.Errorf("case %d (%s): backend result mismatch", index, input.Name)
		}
		if nfaProgram.RunExact(input.Data) != dfaProgram.RunExact(input.Data) {
			return fmt.Errorf("case %d (%s): exact result mismatch", index, input.Name)
		}
		if !reflect.DeepEqual(nfaProgram.Spans(input.Data), dfaProgram.Spans(input.Data)) {
			return fmt.Errorf("case %d (%s): span result mismatch", index, input.Name)
		}
		for _, limit := range []int{1, 2, 3} {
			if !reflect.DeepEqual(nfaProgram.SpansLimit(input.Data, limit), dfaProgram.SpansLimit(input.Data, limit)) {
				return fmt.Errorf("case %d (%s): limit %d mismatch", index, input.Name, limit)
			}
		}
		for from := 0; from <= len(input.Data); from++ {
			for to := from; to <= len(input.Data); to++ {
				if !reflect.DeepEqual(nfaProgram.SpansRange(input.Data, from, to), dfaProgram.SpansRange(input.Data, from, to)) {
					return fmt.Errorf("case %d (%s): range [%d,%d) mismatch", index, input.Name, from, to)
				}
			}
		}

	}
	return nil
}
