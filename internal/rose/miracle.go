package rose

import "bytes"

// Miracle 是单角色文字的轻量候选扫描器。
// 它只负责产生经过边界和偏移筛选的候选，完整规则仍由上层确认。
type Miracle struct {
	role Role
}

func dedupStates(states []State) []State {
	seen := make(map[[2]uint64]struct{}, len(states))
	out := states[:0]
	for _, state := range states {
		key := [2]uint64{uint64(state.RoleID), state.Offset}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, state)
	}
	return out
}

func newMiracle(role Role) *Miracle {
	if role.ID == 0 || len(role.Literal) == 0 {
		return nil
	}
	if role.CaseInsensitive && !isASCII(role.Literal) {
		return nil
	}
	return &Miracle{role: role.Clone()}
}

func (m *Miracle) find(data []byte, from, to, limit int) []State {
	if m == nil || from < 0 || to < from || to > len(data) || limit < 0 {
		return nil
	}
	width := len(m.role.Literal)
	if width == 0 || from > to || width > to-from {
		return nil
	}
	out := make([]State, 0)
	for off := from; off+width <= to; {
		var next int
		if m.role.CaseInsensitive {
			// ASCII 折叠可以使用首字节定位后再完整确认。
			want := foldASCII(m.role.Literal[0])
			next = off
			for next+width <= to && foldASCII(data[next]) != want {
				next++
			}
		} else {
			next = bytes.IndexByte(data[off:to-width+1], m.role.Literal[0])
			if next >= 0 {
				next += off
			} else {
				break
			}
		}
		if next+width > to {
			break
		}
		if m.role.Eligible(data, next) {
			out = append(out, State{RoleID: m.role.ID, Offset: uint64(next)})
			if limit > 0 && len(out) >= limit {
				break
			}
		}
		off = next + 1
	}
	return out
}

func foldASCII(value byte) byte {
	if value >= 'A' && value <= 'Z' {
		return value + ('a' - 'A')
	}
	return value
}

func (m *Miracle) Find(data []byte) []State                    { return m.find(data, 0, len(data), 0) }
func (m *Miracle) FindLimit(data []byte, limit int) []State    { return m.find(data, 0, len(data), limit) }
func (m *Miracle) FindRange(data []byte, from, to int) []State { return m.find(data, from, to, 0) }

func (m *Miracle) FindEndRange(data []byte, from, to, limit int) []State {
	if m == nil || from < 0 || to < from || to > len(data) || limit < 0 {
		return nil
	}
	width := len(m.role.Literal)
	start := from - width
	if start < 0 {
		start = 0
	}
	out := make([]State, 0)
	for _, state := range m.find(data, start, to, 0) {
		finish := int(state.Offset) + width
		if finish < from || finish >= to {
			continue
		}
		out = append(out, state)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}
