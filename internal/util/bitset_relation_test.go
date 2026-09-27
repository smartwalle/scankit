package util

import "testing"

func TestBitSetAndCharReachRelations(t *testing.T) {
	var a, b BitSet
	a.Set(1)
	b.Set(1)
	if !a.Equal(b) || a.Not().Has(1) {
		t.Fatal("位集合关系错误")
	}
	var c, d CharReach
	c.Add('a')
	d.AddRange('a', 'z')
	if !c.IsSubset(d) {
		t.Fatal("字符子集判断失败")
	}
	if c.Equal(d) {
		t.Fatal("字符集合误判相等")
	}
}
