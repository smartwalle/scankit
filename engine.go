package scankit

import "sync"

// Engine 将已编译规则的扫描和替换操作组合为统一入口。
type Engine struct {
	scanner *Scanner
	matches sync.Pool
}

// New 创建 Engine 并编译 expressions。
func New(expressions []Expression) (*Engine, error) {
	scanner, err := Compile(expressions)
	if err != nil {
		return nil, err
	}
	return &Engine{scanner: scanner}, nil
}

// Scan 使用当前 Scanner 扫描 data，并按稳定投递顺序返回全部匹配事件。
func (engine *Engine) Scan(data []byte) ([]Match, error) {
	return engine.scanner.Scan(data)
}

// Replace 使用当前 Scanner 查找 data 中的匹配片段，并通过 fn 写入替换内容。
func (engine *Engine) Replace(data []byte, fn ReplaceFunc) ([]byte, error) {
	matches, err := engine.scanIntoReusableMatches(data)
	if err != nil {
		engine.recycleMatches(matches)
		return nil, err
	}
	defer engine.recycleMatches(matches)
	return Replace(data, matches, fn), nil
}

// Mask 使用当前 Scanner 查找 data 中的匹配片段，并用 mask 原地覆盖这些片段。
func (engine *Engine) Mask(data []byte, mask byte) ([]byte, error) {
	matches, err := engine.scanIntoReusableMatches(data)
	if err != nil {
		engine.recycleMatches(matches)
		return data, err
	}
	defer engine.recycleMatches(matches)
	return Mask(data, matches, mask), nil
}

// MaskWith 使用 fn 原地修改 data 中的命中片段，并返回 data。
func (engine *Engine) MaskWith(data []byte, fn MaskFunc) ([]byte, error) {
	matches, err := engine.scanIntoReusableMatches(data)
	if err != nil {
		engine.recycleMatches(matches)
		return data, err
	}
	defer engine.recycleMatches(matches)
	return MaskWith(data, matches, fn), nil
}

func (engine *Engine) scanIntoReusableMatches(data []byte) ([]Match, error) {
	matches, _ := engine.matches.Get().([]Match)
	return engine.scanner.ScanInto(data, matches[:0])
}

func (engine *Engine) recycleMatches(matches []Match) {
	engine.matches.Put(matches[:0])
}
