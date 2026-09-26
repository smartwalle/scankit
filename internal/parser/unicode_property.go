package parser

import (
	"strings"
	"unicode"
)

// UnicodeProperty 是 \p{...} 属性名的解析结果。
//
// 属性名在解析阶段就已固定，规范化与查表因此只需要做一次；扫描与求值热路径
// 逐字符判定时直接调用 match，不再为每个字符重复规范化字符串、也不再查询需要
// 接口装箱的共享缓存。这是 \p{...} 规则在长输入上最大的常数开销来源。
type UnicodeProperty struct {
	match func(r rune) bool
}

// Match 判断码点 r 是否满足该 Unicode 属性。接收者为 nil 或不认识该属性时返回
// false，让调用方在没有解析结果时可以安全回退到按名称解析。
func (p *UnicodeProperty) Match(r rune) bool {
	if p == nil || p.match == nil {
		return false
	}
	return p.match(r)
}

// unicodePropertyResolutions 是属性名到判定函数的只读索引，在包初始化阶段一次
// 建好。键与 [ResolveUnicodeProperty] 使用同一套规范化规则。
var unicodePropertyResolutions = buildUnicodePropertyResolutions()

// buildUnicodePropertyResolutions 汇总标准库的分类、属性、脚本表以及本包特有的
// 简写别名。别名在标准表之后登记，保证 \p{Alpha} 这类写法按「通用分类」解释，
// 与 [SupportedUnicodeProperty] 的判定顺序一致。
func buildUnicodePropertyResolutions() map[string]*UnicodeProperty {
	resolutions := make(map[string]*UnicodeProperty, len(unicode.Categories)+len(unicode.Properties)+len(unicode.Scripts)+16)
	table := func(name string, value *unicode.RangeTable) {
		tableValue := value
		resolutions[normalizeUnicodeProperty(name)] = &UnicodeProperty{match: func(r rune) bool {
			return unicode.Is(tableValue, r)
		}}
	}
	for name, value := range unicode.Categories {
		table(name, value)
	}
	for name, value := range unicode.Properties {
		table(name, value)
	}
	for name, value := range unicode.Scripts {
		table(name, value)
	}
	alias := func(names []string, match func(r rune) bool) {
		property := &UnicodeProperty{match: match}
		for _, name := range names {
			resolutions[name] = property
		}
	}
	alias([]string{"l", "letter", "alpha"}, unicode.IsLetter)
	alias([]string{"n", "number"}, unicode.IsNumber)
	alias([]string{"nd"}, unicode.IsDigit)
	alias([]string{"z", "space", "whitespace"}, unicode.IsSpace)
	alias([]string{"lu", "uppercaseletter"}, unicode.IsUpper)
	alias([]string{"ll", "lowercaseletter"}, unicode.IsLower)
	alias([]string{"lt"}, func(r rune) bool { return unicode.Is(unicode.Lt, r) })
	alias([]string{"lm"}, func(r rune) bool { return unicode.Is(unicode.Lm, r) })
	alias([]string{"lo"}, func(r rune) bool { return unicode.Is(unicode.Lo, r) })
	alias([]string{"m", "mark"}, func(r rune) bool { return unicode.Is(unicode.M, r) })
	alias([]string{"p", "punct"}, func(r rune) bool { return unicode.Is(unicode.P, r) })
	alias([]string{"s", "symbol"}, func(r rune) bool { return unicode.Is(unicode.S, r) })
	alias([]string{"cc", "control"}, func(r rune) bool { return unicode.Is(unicode.Cc, r) })
	alias([]string{"ascii"}, func(r rune) bool { return r < 128 })
	alias([]string{"any"}, func(rune) bool { return true })
	alias([]string{"assigned"}, func(r rune) bool { return !unicode.Is(unicode.Cn, r) })
	alias([]string{"unassigned"}, func(r rune) bool { return unicode.Is(unicode.Cn, r) })
	return resolutions
}

// ResolveUnicodeProperty 把 \p{...} 的属性名解析成可复用的判定结果。
// 名称无法识别时返回 nil。
func ResolveUnicodeProperty(name string) *UnicodeProperty {
	return unicodePropertyResolutions[unicodePropertyKey(name)]
}

// unicodePropertyKey 复现属性名的规范化与去前缀流程：统一小写、去掉下划线、
// 连字符与空白，再剥离 script=/sc=/generalcategory=/gc= 以及 is/in 前缀。
func unicodePropertyKey(name string) string {
	key := normalizeUnicodeProperty(name)
	for _, prefix := range []string{"script=", "sc=", "script:", "generalcategory=", "gc="} {
		if after, ok := strings.CutPrefix(key, prefix); ok {
			key = after
			break
		}
	}
	if len(key) > 2 && (strings.HasPrefix(key, "is") || strings.HasPrefix(key, "in")) {
		key = key[2:]
	}
	return key
}
