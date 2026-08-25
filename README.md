# scankit

`scankit` 是一个纯 Go 的多规则扫描库：在编译阶段将多条 `Expression` 合并为不可变的 `Scanner`，随后以一次前向扫描在连续 `[]byte` 中交付匹配结果。它适用于日志扫描、内容识别和脱敏前的命中定位。

## 安装

需要 Go 1.26 或更高版本。

```sh
go get github.com/smartwalle/scankit
```

## 快速开始

```go
import (
	"fmt"
	"log"

	"github.com/smartwalle/scankit"
)

scanner, err := scankit.Compile([]scankit.Expression{
	{ID: 1, Pattern: `\b\d{11}\b`},
	{ID: 2, Pattern: `[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`},
})
if err != nil {
	log.Fatal(err)
}

data := []byte("phone=13800138000 email=alice@example.com")
matches, err := scanner.Scan(data)
if err != nil {
	log.Fatal(err)
}
for _, match := range matches {
	fmt.Printf("id=%d value=%q range=[%d,%d)\n", match.ID,
		data[match.From:match.To], match.From, match.To)
}
```

`Match.From` 和 `Match.To` 是字节偏移量，区间为 `[From, To)`。同一表达式在同一结束位置最多交付一个事件，并稳定保留最左的有效起点。`Scanner` 可由多个 goroutine 并发共享；如需复用结果切片，请使用 `ScanInto`。

```go
matches = matches[:0]
matches, err = scanner.ScanInto(data, matches)
```

## 已支持的表达式

`Pattern` 默认采用字节正则语义。非 ASCII 文本、Unicode 属性和 Unicode 大小写折叠需要同时设置 `CompileUTF8 | CompileUnicodeProperties`；仅设置 `CompileUTF8` 时只接受合法 UTF-8 的纯字面量。

### 字节模式

| 分类 | 支持的语法 | 示例 |
| --- | --- | --- |
| 字面量与连接 | 普通字面量、连接 | `token=abc` |
| 字符类 | 类、否定类、范围、POSIX 类 | `[A-F0-9]`、`[^\r\n]`、`[[:digit:]]`、`[[:^space:]]` |
| 交替与分组 | `|`、捕获分组、非捕获分组、命名分组 | `(foo|bar)`、`(?:foo)`、`(?<name>foo)`、`(?P<name>foo)` |
| 重复 | `?`、`*`、`+`、`{n}`、`{n,}`、`{n,m}`；可附加 lazy 后缀 `?` | `\d{2,4}`、`a+?` |
| 通配与锚点 | `.`、`\N`、`^`、`$`、`\A`、`\z`、`\Z`、`\b`、`\B` | `^GET .+`、`\btoken\b` |
| 常用转义类 | ASCII `\d`/`\D`、`\w`/`\W`、`\s`/`\S`、`\h`/`\H`、`\v`/`\V`、`\R` | `\R`、`\h+` |
| 字面量与字符转义 | `\Q...\E`、`\a`、`\e`、`\cX`、`\0NNN`、`\xHH`、`\x{...}`、`\o{...}` | `\Quser.+\E`、`\x{2f}` |
| 注释与局部修饰符 | 注释分组、内联及作用域 `i`、`m`、`s`、`x` | `(?#note)foo`、`(?i:token)`、`(?x: foo bar )` |

字节模式中的字符类和简写均是 ASCII/字节语义。例如 `\d` 等价于 `[0-9]`，不匹配其他 Unicode 数字。

### Unicode 模式

为表达式同时设置 `CompileUTF8 | CompileUnicodeProperties` 后，模式按 rune 执行，输入必须是合法 UTF-8。除上表中适用的分组、交替、重复、锚点、转义和内联修饰符外，还支持：

| 分类 | 支持的语法 | 示例 |
| --- | --- | --- |
| Unicode 字面量与类 | 非 ASCII 字面量、Unicode 字符类及其补集 | `姓名`、`[^甲]` |
| Unicode 属性 | `\p{...}`、`\P{...}`、`\p{^...}`；常见别名以及 `Any`、`Assigned`、`ASCII` | `\p{Han}+`、`\p{Nd}{2}`、`\p{^L}` |
| Unicode POSIX 类 | `[[:name:]]` 和 `[[:^name:]]`，包括 `alnum`、`alpha`、`ascii`、`blank`、`cntrl`、`digit`、`graph`、`lower`、`print`、`punct`、`space`、`upper`、`word`、`xdigit` | `[[:alpha:]]+`、`[[:^digit:]]` |
| Unicode 简写 | rune 语义的 `\d`、`\w`、`\s`、`\h`、`\v` 及其否定形式 | `\w+`、`甲\h乙` |
| Unicode 特有行为 | Unicode `.`、`\N`、`\Z`、`\R`，以及 `\x{...}`、`\o{...}` | `用户\Z`、`\x{4E2D}` |
| 大小写无关 | Unicode simple fold | `Σ+` 配合 `CompileCaseless` |

## 表达式标志

| 标志 | 含义 |
| --- | --- |
| `CompileCaseless` | 忽略大小写。字节模式为 ASCII；配合 `CompileUTF8 | CompileUnicodeProperties` 时使用 Unicode simple fold。 |
| `CompileDotAll` | 使 `.` 匹配换行。 |
| `CompileMultiline` | 使 `^` 和 `$` 按行工作。 |
| `CompileSingleMatch` | 每条表达式最多交付一次匹配。 |
| `CompileLeftmostStart` | 同一结束位置保留最左起点。 |
| `CompileAllowEmpty` | 允许可匹配空串的表达式。 |
| `CompileUTF8` | 校验输入和模式的 UTF-8；Unicode 正则需与 `CompileUnicodeProperties` 一同使用。 |
| `CompileUnicodeProperties` | 启用 Unicode 属性与 rune 语义；必须同时设置 `CompileUTF8`。 |
| `CompilePrefilter` | 对已支持语法仍使用精确执行，不扩大结果集。 |
| `CompileQuiet` | 规则仍可作为组合规则的操作数，但不单独交付结果。 |
| `CompileCombination` | 将 `Pattern` 解释为表达式 ID 的布尔组合，而非正则。 |

组合规则使用 `!`、`&`、`|` 和括号，例如 `1&!2`。操作数为同一个 `Scanner` 中的表达式 ID；组合规则不能引用另一个组合规则。

## 结果限制与近似匹配

通过 `ExpressionExt` 可以设置匹配结果的限制：

| 扩展标志 | 作用 |
| --- | --- |
| `ExtMinOffset` / `ExtMaxOffset` | 限制 `Match.To` 的最小/最大字节偏移。 |
| `ExtMinLength` | 限制 `Match.To - Match.From` 的最小长度（字节）。 |
| `ExtHammingDistance` | 精确 Hamming 距离；仅限非空、固定宽度的字节模式，或受资源限制的固定宽度 UCP rune 图。 |
| `ExtEditDistance` | 精确 Edit 距离；仅限非空、有界、无断言且不含 `\R` 的字节 NFA 或 UCP rune 图。 |

近似匹配的最大宽度为 256 byte/rune、最大距离为 64、编译后状态上限为 512。超出这些资源限制，或使用无界/可空语言时，`Compile` 返回 `ErrUnsupportedExtension`，不会使用不精确回退。

## 明确不支持的语法

以下语法会在编译期被拒绝，不会被当作普通字面量处理：

- `\C`、`\K`、非零数值转义与反向引用
- 子程序、递归、条件、lookaround
- 原子组与占有量词
- `\X`、`\N{name}`
- 未列出的 PCRE 或 Unicode 构造，以及未配合 `CompileUTF8` 的 `CompileUnicodeProperties`

请检查并处理 `Compile` 返回的错误。常见的可分类错误包括 `ErrUnsupportedExpression`、`ErrUnsupportedFlag`、`ErrRegexTooComplex` 和 `ErrUnsupportedExtension`。
