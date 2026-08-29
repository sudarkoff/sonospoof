package alac

// Adaptive Golomb decode, ported from Apple's ag_dec.c.
//
// Kept in unsigned 32-bit arithmetic throughout because the original relies
// on C's wraparound in at least one place: in dynGet, v==0 makes `result-1`
// underflow and the following `result -= v-1` adds it straight back. Rewriting
// that "more clearly" in signed arithmetic changes the answer.

const (
	qbShift = 9
	qb      = 1 << qbShift
	pb0     = 40
	mb0     = 10
	kb0     = 14

	maxRunDefault = 255

	mmulShift = 2
	mdenShift = qbShift - mmulShift - 1
	moff      = 1 << (mdenShift - 2)

	bitOff = 24

	maxPrefix16       = 9
	maxPrefix32       = 9
	maxDatatypeBits16 = 16

	nMaxMeanClamp  = 0xffff
	nMeanClampVal  = 0xffff
	codeToLongBits = 32
)

// agParams mirrors AGParamRec.
type agParams struct {
	mb, mb0, pb, kb, wb, qbv uint32
	fw, sw                   uint32
	maxrun                   uint32
}

func setAGParams(p *agParams, m, pbv, k, f, s, maxrun uint32) {
	p.mb = m
	p.mb0 = m
	p.pb = pbv
	p.kb = k
	p.wb = (1 << p.kb) - 1
	p.qbv = qb - p.pb
	p.fw = f
	p.sw = s
	p.maxrun = maxrun
}

// lead counts leading zero bits, matching the reference's loop exactly
// (including that lead(0) == 32).
func lead(m uint32) uint32 {
	c := uint32(1) << 31
	for j := uint32(0); j < 32; j++ {
		if c&m != 0 {
			return j
		}
		c >>= 1
	}
	return 32
}

func lg3a(x uint32) uint32 {
	x += 3
	return 31 - lead(x)
}

// read32bit reads four big-endian bytes at a byte offset, zero-filling past
// the end. The reference always loads 32 bits regardless of how many it needs,
// so reads near the end of a frame routinely overrun.
func read32bit(in []byte, off int) uint32 {
	var v uint32
	for i := 0; i < 4; i++ {
		v <<= 8
		if j := off + i; j >= 0 && j < len(in) {
			v |= uint32(in[j])
		}
	}
	return v
}

func byteAt(in []byte, i int) uint32 {
	if i < 0 || i >= len(in) {
		return 0
	}
	return uint32(in[i])
}

func getNextFromLong(inlong uint32, suff uint32) uint32 {
	if suff == 0 {
		return 0
	}
	return inlong >> (32 - suff)
}

func getStreamBits(in []byte, bitoffset int, numbits uint32) uint32 {
	byteoffset := bitoffset / 8
	load1 := read32bit(in, byteoffset)
	shift := uint32(bitoffset & 7)

	var result uint32
	if numbits+shift > 32 {
		result = load1 << shift
		load2 := byteAt(in, byteoffset+4)
		load2shift := 8 - (numbits + shift - 32)
		load2 >>= load2shift
		result >>= 32 - numbits
		result |= load2
	} else {
		result = load1 >> (32 - numbits - shift)
	}

	// Shifting by the full width is undefined in C and panics differently in
	// Go, so the reference guards it and so do we.
	if numbits != 32 {
		result &^= 0xffffffff << numbits
	}
	return result
}

func dynGet(in []byte, bitPos *int, m, k uint32) uint32 {
	tempbits := *bitPos
	streamlong := read32bit(in, tempbits>>3) << uint32(tempbits&7)

	pre := lead(^streamlong)

	var result uint32
	if pre >= maxPrefix16 {
		pre = maxPrefix16
		tempbits += int(pre)
		streamlong <<= pre
		result = getNextFromLong(streamlong, maxDatatypeBits16)
		tempbits += maxDatatypeBits16
	} else {
		tempbits += int(pre) + 1
		streamlong <<= pre + 1
		v := getNextFromLong(streamlong, k)
		tempbits += int(k)

		result = pre*m + v - 1
		if v < 2 {
			result -= v - 1
			tempbits--
		}
	}

	*bitPos = tempbits
	return result
}

func dynGet32bit(in []byte, bitPos *int, m, k, maxbits uint32) uint32 {
	tempbits := *bitPos
	streamlong := read32bit(in, tempbits>>3) << uint32(tempbits&7)

	result := lead(^streamlong)

	if result >= maxPrefix32 {
		result = getStreamBits(in, tempbits+maxPrefix32, maxbits)
		tempbits += maxPrefix32 + int(maxbits)
	} else {
		tempbits += int(result) + 1

		if k != 1 {
			streamlong <<= result + 1
			v := getNextFromLong(streamlong, k)
			tempbits += int(k) - 1
			result *= m
			if v >= 2 {
				result += v - 1
				tempbits++
			}
		}
	}

	*bitPos = tempbits
	return result
}

// dynDecomp decodes numSamples residuals into pc. It returns the number of
// bits consumed so the caller can advance its own reader.
func dynDecomp(p *agParams, bits *bitBuffer, pc []int32, numSamples int, maxSize uint32) (int, error) {
	in := bits.buf
	startPos := bits.position()
	maxPos := len(in) * 8
	bitPos := startPos

	mb := p.mb0
	pbLocal := p.pb
	kbLocal := p.kb
	wbLocal := p.wb

	var zmode uint32
	c := 0

	for c < numSamples {
		if bitPos >= maxPos {
			bits.advance(bitPos - startPos)
			return bitPos - startPos, errTruncated
		}

		m := mb >> qbShift
		k := lg3a(m)
		if k > kbLocal {
			k = kbLocal
		}
		m = (1 << k) - 1

		n := dynGet32bit(in, &bitPos, m, k, maxSize)

		// Least significant bit of (n+zmode) is the sign.
		ndecode := n + zmode
		multiplier := int32(-int32(ndecode & 1))
		multiplier |= 1
		pc[c] = int32((ndecode+1)>>1) * multiplier

		c++

		mb = pbLocal*(n+zmode) + mb - ((pbLocal * mb) >> qbShift)

		if n > nMaxMeanClamp {
			mb = nMeanClampVal
		}

		zmode = 0

		if (mb<<mmulShift) < qb && c < numSamples {
			zmode = 1
			k = lead(mb) - bitOff + ((mb + moff) >> mdenShift)
			mz := ((1 << k) - 1) & wbLocal

			n = dynGet(in, &bitPos, mz, k)

			if c+int(n) > numSamples {
				bits.advance(bitPos - startPos)
				return bitPos - startPos, errCorruptRun
			}

			for j := uint32(0); j < n; j++ {
				pc[c] = 0
				c++
			}

			if n >= 65535 {
				zmode = 0
			}
			mb = 0
		}
	}

	used := bitPos - startPos
	bits.advance(used)
	return used, nil
}
