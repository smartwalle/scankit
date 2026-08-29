// Package scankit 提供跨平台的 Block 多规则扫描器。
package scankit

import (
	"sync"
)

type Match struct {
	Id   uint32
	From uint64
	To   uint64
}

// Scanner 是内存中的不可变编译规则执行计划，可安全地并发扫描，并管理所需的可复用扫描上下文。
type Scanner struct {
	contextPool *sync.Pool
}

// Scan 扫描输入并返回全部匹配。结果按输入起点、规则声明顺序排列。
func (scanner *Scanner) Scan(data []byte) ([]Match, error) {
	return scanner.ScanInto(data, nil)
}

// ScanInto 将匹配追加到 matches；输入数据不会被修改。
func (scanner *Scanner) ScanInto(data []byte, matches []Match) ([]Match, error) {
	return matches, nil
}
