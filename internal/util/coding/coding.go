// Package coding provides utilities for encoding and decoding binary data.
package coding

import (
	"encoding/binary"
	"io"
)

// ByteReader wraps a byte slice and tracks read position.
// It provides safe reading with bounds checking.
type ByteReader struct {
	data   []byte
	offset int
}

// NewByteReader creates a reader over the given data.
func NewByteReader(data []byte) *ByteReader {
	return &ByteReader{data: data, offset: 0}
}

// Remaining returns how many bytes are left to read.
func (r *ByteReader) Remaining() int {
	return len(r.data) - r.offset
}

// ReadByte reads a single byte.
func (r *ByteReader) ReadByte() (byte, error) {
	if r.offset >= len(r.data) {
		return 0, io.ErrUnexpectedEOF
	}
	b := r.data[r.offset]
	r.offset++
	return b, nil
}

// ReadUint32 reads a big-endian uint32.
func (r *ByteReader) ReadUint32() (uint32, error) {
	if r.offset+4 > len(r.data) {
		return 0, io.ErrUnexpectedEOF
	}
	v := Uint32(r.data[r.offset:])
	r.offset += 4
	return v, nil
}

// ReadBytes reads exactly n bytes.
func (r *ByteReader) ReadBytes(n int) ([]byte, error) {
	if r.offset+n > len(r.data) {
		return nil, io.ErrUnexpectedEOF
	}
	b := r.data[r.offset : r.offset+n]
	r.offset += n
	return b, nil
}

// PutUint16 stores v into dst[0:2]
func PutUint16(dst []byte, v uint16) {
	binary.BigEndian.PutUint16(dst, v)
}

// Uint16 returns the uint16 representation of src[0:2]
func Uint16(src []byte) uint16 {
	return binary.BigEndian.Uint16(src)
}

// PutUint32 stores v into dst[0:4]
func PutUint32(dst []byte, v uint32) {
	binary.BigEndian.PutUint32(dst, v)
}

// Uint32 returns the uint32 representation of src[0:4]
func Uint32(src []byte) uint32 {
	return binary.BigEndian.Uint32(src)
}

// PutUint64 stores v into dst[0:8]
func PutUint64(dst []byte, v uint64) {
	binary.BigEndian.PutUint64(dst, v)
}

// Uint64 returns the uint64 representation of src[0:8]
func Uint64(src []byte) uint64 {
	return binary.BigEndian.Uint64(src)
}
