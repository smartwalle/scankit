//go:build race

package scankit

// raceEnabled 表示当前测试二进制启用了竞态检测。
// 竞态插桩会额外分配，性能相关的分配断言在启用时需要放宽。
const raceEnabled = true
