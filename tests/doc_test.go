// Package tests 是 scankit 的黑盒测试包：只通过公开 API（Compile、New、
// Scanner.Scan、Scanner.ScanInto、Engine.Validate/Scan/Replace/Mask、Replace、
// Mask）验证行为，不引用根包的私有标识符（函数、方法、字段）。
//
// 唯一例外是跨后端一致性与逐后端基准：公开 API 没有指定 SIMD 后端的手段，
// 这些用例通过 internal/dispatch 的 SetBackendOverride 与 internal/simd 的
// Backend 实现强制指定后端（见 scan_test.go、bench_test.go）。
//
// 目录按主题组织：
//
//   - syntax_test.go：规则语法与语义验证（单条规则、组合规则、真实业务规则）；
//   - compile_test.go：编译契约、错误哨兵与资源限制；
//   - scan_test.go：扫描/替换契约与跨后端一致性；
//   - pii_test.go：日志场景的 PII 扫描与脱敏；
//   - bench_test.go：吞吐基准与稳定基准。
package tests
