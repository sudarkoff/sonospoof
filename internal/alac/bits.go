// Package alac decodes Apple Lossless audio.
//
// It is a port of Apple's reference decoder (Apache 2.0), kept deliberately
// close to the original structure so it can be diffed against it: the same
// function names, the same order of operations, the same magic numbers. The
// original is C with pointer arithmetic and relies on reading a few bytes
// past the current position; where that is unsafe in Go the bounds are
// checked and the reads return zero, which matches what the C code would
// have found in a zero-padded buffer.
package alac

// bitBuffer is a big-endian bit reader over a fixed byte slice.
type bitBuffer struct {
	buf []byte
	// pos is the bit offset from the start of buf.
	pos int
}

func newBitBuffer(b []byte) *bitBuffer { return &bitBuffer{buf: b} }

// byteAt returns buf[i], or 0 past the end. The reference decoder reads three
// bytes at a time regardless of how many bits it needs, so the tail of a
// frame routinely reads past the last byte; C got zeros from the caller's
// padding and so do we.
func (b *bitBuffer) byteAt(i int) uint32 {
	if i < 0 || i >= len(b.buf) {
		return 0
	}
	return uint32(b.buf[i])
}

// read returns the next n bits, most significant first. n must be <= 32.
func (b *bitBuffer) read(n uint32) uint32 {
	if n == 0 {
		return 0
	}
	if n > 16 {
		// Split so the 24-bit window below always suffices.
		hi := b.read(n - 16)
		lo := b.read(16)
		return hi<<16 | lo
	}

	i := b.pos >> 3
	shift := uint(b.pos & 7)

	v := b.byteAt(i)<<16 | b.byteAt(i+1)<<8 | b.byteAt(i+2)
	v = (v << shift) & 0x00FFFFFF
	v >>= 24 - n

	b.pos += int(n)
	return v
}

// peek returns the next n bits without consuming them. n must be <= 16.
func (b *bitBuffer) peek(n uint32) uint32 {
	save := b.pos
	v := b.read(n)
	b.pos = save
	return v
}

// advance skips n bits.
func (b *bitBuffer) advance(n int) { b.pos += n }

// position is the current bit offset, used to size the shift-bits region.
func (b *bitBuffer) position() int { return b.pos }

// byteAlign moves to the next byte boundary.
func (b *bitBuffer) byteAlign() {
	if r := b.pos & 7; r != 0 {
		b.pos += 8 - r
	}
}

// exhausted reports whether the reader has consumed every bit it holds. The
// decoder uses this to fail loudly on a truncated frame rather than silently
// producing zeros for the remainder.
func (b *bitBuffer) exhausted() bool { return b.pos > len(b.buf)*8 }
