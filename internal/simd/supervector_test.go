package simd

import (
	"testing"

	"github.com/smartwalle/scankit/internal/simd/generic"
)

func TestSuperVectorLoadStoreAndMasks(t *testing.T) {
	data := []byte("abcdefghijklmnopqrstuvwxyz012345")
	v, ok := LoadSuper(data, 0)
	if !ok || !v.AnyEqual('z') {
		t.Fatalf("超向量加载或查找失败: ok=%v", ok)
	}
	if _, ok := LoadSuper(data, 1); ok {
		t.Fatal("非完整超向量未拒绝")
	}
	partial := PartialLoadSuper(data, len(data)-1)
	lo, hi := partial.EqualByteMask(data[len(data)-1])
	if lo == 0 && hi == 0 {
		t.Fatal("部分加载未保留尾部字节")
	}
	out := make([]byte, len(data))
	if !v.Store(out, 0) || string(out) != string(data) {
		t.Fatalf("超向量写回错误: %q", out)
	}
	if v.Store(out, 1) {
		t.Fatal("超向量越界写回未拒绝")
	}
}

func TestSuperVectorBackendAwareOperations(t *testing.T) {
	data := make([]byte, SuperWidth+1)
	for i := range data {
		data[i] = byte(i)
	}
	b := GenericBackend{}
	v, ok := LoadSuperWithBackend(b, data, 1)
	if !ok || v.Lo[0] != 1 || v.Hi[0] != 1+generic.Width {
		t.Fatalf("分派超向量加载错误: ok=%v lo=%d hi=%d", ok, v.Lo[0], v.Hi[0])
	}
	out := make([]byte, SuperWidth+1)
	if !v.StoreWithBackend(b, out, 1) || string(out[1:]) != string(data[1:]) {
		t.Fatal("分派超向量写回错误")
	}
}
