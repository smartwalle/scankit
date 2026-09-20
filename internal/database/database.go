// Package database 保存不可变的编译程序。
package database

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"runtime"
)

const Version = 1

const (
	maxDatabaseBytes    = 64 << 20
	maxDatabasePrograms = 1 << 20
)

type Database[T any] struct {
	Programs     []T    `json:"programs"`
	Version      int    `json:"version"`
	Architecture string `json:"architecture"`
	ScratchBytes uint64 `json:"scratch_bytes"`
	Digest       string `json:"digest,omitempty"`
}

func New[T any](programs []T) *Database[T] {
	return &Database[T]{Programs: append([]T(nil), programs...), Version: Version, Architecture: runtime.GOARCH}
}

func (d *Database[T]) Validate() error {
	if d == nil {
		return fmt.Errorf("nil database")
	}
	if d.Version != Version {
		return fmt.Errorf("unsupported database version %d", d.Version)
	}
	if len(d.Programs) > maxDatabasePrograms || d.ScratchBytes > maxDatabaseBytes {
		return fmt.Errorf("database exceeds resource limits")
	}
	if d.Architecture != "" && d.Architecture != runtime.GOARCH {
		return fmt.Errorf("database architecture %q is incompatible", d.Architecture)
	}
	return nil
}

// ValidatePrograms 使用调用方校验器逐项验证程序集合。
func (d *Database[T]) ValidatePrograms(check func(T) error) error {
	if d == nil {
		return fmt.Errorf("nil database")
	}
	if err := d.Validate(); err != nil {
		return err
	}
	if check == nil {
		return nil
	}
	for index, program := range d.Programs {
		if err := check(program); err != nil {
			return fmt.Errorf("program %d: %w", index, err)
		}
	}
	return nil
}

func (d *Database[T]) Marshal() ([]byte, error) {
	if err := d.Validate(); err != nil {
		return nil, err
	}
	copyValue := *d
	copyValue.Digest = ""
	raw, err := json.Marshal(copyValue)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(raw)
	copyValue.Digest = fmt.Sprintf("%x", digest[:])
	out, err := json.Marshal(copyValue)
	if err != nil {
		return nil, err
	}
	// 序列化成功后同步摘要，便于调用方复用对象时读取当前摘要。
	d.Digest = copyValue.Digest
	return out, nil
}

// VerifyDigest 校验对象当前内容是否与已保存摘要一致。
func (d *Database[T]) VerifyDigest() bool {
	if d == nil || d.Digest == "" || d.Validate() != nil {
		return false
	}
	got := d.Digest
	copyValue := *d
	copyValue.Digest = ""
	raw, err := json.Marshal(copyValue)
	if err != nil {
		return false
	}
	digest := sha256.Sum256(raw)
	return got == fmt.Sprintf("%x", digest[:])
}

// ValidateDigest 返回摘要缺失或内容不一致的具体错误。
func (d *Database[T]) ValidateDigest() error {
	if d == nil {
		return fmt.Errorf("nil database")
	}
	if err := d.Validate(); err != nil {
		return err
	}
	if d.Digest == "" {
		return fmt.Errorf("database digest is missing")
	}
	if !d.VerifyDigest() {
		return fmt.Errorf("database digest mismatch")
	}
	return nil
}
func Unmarshal[T any](data []byte) (*Database[T], error) {
	if len(data) > maxDatabaseBytes {
		return nil, fmt.Errorf("database payload exceeds size limit")
	}
	var d Database[T]
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&d); err != nil {
		return nil, err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("database payload contains multiple values")
		}
		return nil, err
	}
	if err := d.Validate(); err != nil {
		return nil, err
	}
	if d.Digest != "" {
		got := d.Digest
		d.Digest = ""
		raw, err := json.Marshal(d)
		if err != nil {
			return nil, err
		}
		digest := sha256.Sum256(raw)
		if got != fmt.Sprintf("%x", digest[:]) {
			return nil, fmt.Errorf("database digest mismatch")
		}
		d.Digest = got
	}
	return &d, nil
}
func (d *Database[T]) Len() int {
	if d == nil {
		return 0
	}
	return len(d.Programs)
}
func (d *Database[T]) Clone() *Database[T] {
	if d == nil {
		return nil
	}
	out := *d
	out.Programs = append([]T(nil), d.Programs...)
	return &out
}
func (d *Database[T]) ScratchRequirement() uint64 {
	if d == nil {
		return 0
	}
	return d.ScratchBytes
}
func (d *Database[T]) ArchitectureCompatible() bool {
	return d != nil && (d.Architecture == "" || d.Architecture == runtime.GOARCH)
}

// CompatibleWith 判断两个数据库是否可在同一运行时安全交换。
func (d *Database[T]) CompatibleWith(other *Database[T]) bool {
	if d == nil || other == nil || d.Validate() != nil || other.Validate() != nil {
		return false
	}
	return d.Architecture == "" || other.Architecture == "" || d.Architecture == other.Architecture
}

// ProgramCount 返回数据库中的程序数量，与 Len 保持语义一致。
func (d *Database[T]) ProgramCount() int { return d.Len() }

// ScratchSufficient 判断提供的 scratch 容量是否满足数据库声明。
func (d *Database[T]) ScratchSufficient(bytes uint64) bool {
	return d != nil && bytes >= d.ScratchBytes
}

// ArchitectureValue 返回数据库目标架构。
func (d *Database[T]) ArchitectureValue() string {
	if d == nil {
		return ""
	}
	return d.Architecture
}

// ProgramAt 返回指定位置的程序副本。
func (d *Database[T]) ProgramAt(index int) (T, bool) {
	var zero T
	if d == nil || index < 0 || index >= len(d.Programs) {
		return zero, false
	}
	return d.Programs[index], true
}

// ReplaceProgram 替换指定位置的程序并清除摘要。
func (d *Database[T]) ReplaceProgram(index int, program T) bool {
	if d == nil || index < 0 || index >= len(d.Programs) {
		return false
	}
	d.Programs[index] = program
	d.Digest = ""
	return true
}

// AppendProgram 追加程序并清除摘要。
func (d *Database[T]) AppendProgram(program T) bool {
	if d == nil {
		return false
	}
	d.Programs = append(d.Programs, program)
	d.Digest = ""
	return true
}

// RefreshDigest 重新序列化并更新摘要，返回摘要值。
func (d *Database[T]) RefreshDigest() (string, error) {
	if d == nil {
		return "", fmt.Errorf("nil database")
	}
	if _, err := d.Marshal(); err != nil {
		return "", err
	}
	return d.Digest, nil
}
func (d *Database[T]) SetScratchRequirement(bytes uint64) {
	if d != nil {
		d.ScratchBytes = bytes
		d.Digest = ""
	}
}

// SetArchitecture 设置目标架构并清除旧摘要。
func (d *Database[T]) SetArchitecture(architecture string) {
	if d == nil {
		return
	}
	d.Architecture = architecture
	d.Digest = ""
}

// SetArchitectureCurrent 将数据库目标架构设置为当前运行架构。
func (d *Database[T]) SetArchitectureCurrent() {
	if d != nil {
		d.SetArchitecture(runtime.GOARCH)
	}
}

// SetPrograms 替换程序集合并清除旧摘要。
func (d *Database[T]) SetPrograms(programs []T) {
	if d == nil {
		return
	}
	d.Programs = append(d.Programs[:0], programs...)
	d.Digest = ""
}
func (d *Database[T]) ProgramsCopy() []T {
	if d == nil {
		return nil
	}
	return append([]T(nil), d.Programs...)
}

// ClearPrograms 清空程序集合并清除旧摘要。
func (d *Database[T]) ClearPrograms() {
	if d != nil {
		d.Programs = d.Programs[:0]
		d.Digest = ""
	}
}
func (d *Database[T]) Empty() bool { return d == nil || len(d.Programs) == 0 }
func (d *Database[T]) VersionValue() int {
	if d == nil {
		return 0
	}
	return d.Version
}
func (d *Database[T]) IsCurrent() bool { return d != nil && d.Version == Version }

// DigestValue 返回当前序列化摘要；未序列化或已修改时为空。
func (d *Database[T]) DigestValue() string {
	if d == nil {
		return ""
	}
	return d.Digest
}
