package dispatch

import (
	"sync"

	"github.com/smartwalle/scankit/internal/simd"
)

// 后端覆盖用于在不改变编译期探测结果的前提下强制使用某个后端，
// 便于在目标平台复核原生路径与通用回退路径的一致性。
var backendOverride = struct {
	sync.RWMutex
	value simd.Backend
}{}

// SetBackendOverride 覆盖后续 DefaultBackend 的返回值。
// 传入 nil 表示恢复按 CPU 能力探测的后端；覆盖只影响之后发起的扫描。
func SetBackendOverride(backend simd.Backend) {
	backendOverride.Lock()
	defer backendOverride.Unlock()
	backendOverride.value = backend
}

// BackendOverride 返回当前强制使用的后端，nil 表示未设置覆盖。
func BackendOverride() simd.Backend {
	backendOverride.RLock()
	defer backendOverride.RUnlock()
	return backendOverride.value
}
