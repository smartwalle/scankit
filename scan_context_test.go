package scankit

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestScanContextReturnsResults 验证 ScanContext 在未取消时与 Scan 行为一致。
func TestScanContextReturnsResults(t *testing.T) {
	s, err := Compile([]Expression{{Id: 1, Pattern: `abc`}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	matches, err := s.ScanContext(ctx, []byte("abc abc"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 2 {
		t.Fatalf("ScanContext 应匹配 2 个，实际=%d", len(matches))
	}
}

// TestScanContextCancelledBeforeCall 验证 ScanContext 在 ctx 已取消时立即返回 ErrCancelled。
func TestScanContextCancelledBeforeCall(t *testing.T) {
	s, err := Compile([]Expression{{Id: 1, Pattern: `abc`}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	matches, err := s.ScanContext(ctx, []byte("abc abc"))
	if !errors.Is(err, ErrCancelled) {
		t.Fatalf("期望 ErrCancelled，实际=%v", err)
	}
	if matches != nil {
		t.Fatalf("已取消上下文应返回 nil matches，实际=%v", matches)
	}
}

// TestScanContextNilContext 验证 ScanContext 在 ctx==nil 时等价于 Scan。
func TestScanContextNilContext(t *testing.T) {
	s, err := Compile([]Expression{{Id: 1, Pattern: `abc`}})
	if err != nil {
		t.Fatal(err)
	}
	matches, err := s.ScanContext(nil, []byte("abc abc"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 2 {
		t.Fatalf("nil ctx 应匹配 2 个，实际=%d", len(matches))
	}
}

// TestScanContextIntoReusesBuffer 验证 ScanContextInto 复用 matches 缓冲并遵守取消。
func TestScanContextIntoReusesBuffer(t *testing.T) {
	s, err := Compile([]Expression{{Id: 1, Pattern: `abc`}})
	if err != nil {
		t.Fatal(err)
	}
	dst := make([]Match, 0, 8)
	matches, err := s.ScanContextInto(context.Background(), []byte("abc abc"), dst)
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 2 {
		t.Fatalf("ScanContextInto 应匹配 2 个，实际=%d", len(matches))
	}
}

// TestScanContextTimeout 验证带超时的 ctx 在超时后被识别为已取消。
func TestScanContextTimeout(t *testing.T) {
	s, err := Compile([]Expression{{Id: 1, Pattern: `abc`}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Millisecond)
	defer cancel()
	time.Sleep(5 * time.Millisecond)
	matches, err := s.ScanContext(ctx, []byte("abc abc"))
	if !errors.Is(err, ErrCancelled) {
		t.Fatalf("超时 ctx 应返回 ErrCancelled，实际=%v", err)
	}
	if matches != nil {
		t.Fatalf("已超时应返回 nil matches，实际=%v", matches)
	}
}

// TestScanContextCancelledDuringLongScan 验证长输入扫描中触发的取消
// 能在主扫描循环的 1024 起点周期探测点被捕获，立即返回 ErrCancelled 与已收集结果。
func TestScanContextCancelledDuringLongScan(t *testing.T) {
	// 强制走主循环：使用含字符类的非纯文字规则。
	s, err := Compile([]Expression{{Id: 1, Pattern: `[a-z]+`}})
	if err != nil {
		t.Fatal(err)
	}
	// 构造 64KB 数据，让主循环至少跨过 60 个 1024 起点周期。
	data := make([]byte, 0, 64*1024)
	buf := make([]byte, 1024)
	for i := range buf {
		buf[i] = byte('a' + i%26)
	}
	for i := 0; i < 64; i++ {
		data = append(data, buf...)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(1 * time.Microsecond)
		cancel()
	}()
	matches, err := s.ScanContext(ctx, data)
	if !errors.Is(err, ErrCancelled) {
		t.Fatalf("期望 ErrCancelled，实际=%v", err)
	}
	_ = matches
}
