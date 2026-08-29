package alac

// Adaptive predictor, ported from Apple's dp_dec.c (unpc_block).
//
// The reference has hand-unrolled fast paths for numactive 4 and 8 alongside
// a general loop. Only the general loop is ported here: the unrolled versions
// compute b_k = top - pout[-k] and sum1 = (denhalf - Σ a_k·b_k) >> denshift,
// while the general one computes sum1 = Σ coefs[k]·(pout[-k] - top) and then
// (sum1 + denhalf) >> denshift. Since coefs[k]·(pout[-k] - top) == -a_k·b_k,
// the two are identical; the unrolling was for the G5, not for correctness.
//
// coefs is updated in place -- the predictor adapts as it runs, so the same
// slice must be carried across the whole frame.

// signOfInt returns 1, 0 or -1. Written as the reference's bit trick rather
// than a comparison so that i == math.MinInt32, where -i overflows, gives the
// same answer the C does.
func signOfInt(i int32) int32 {
	negishift := int32(uint32(-i) >> 31)
	return negishift | (i >> 31)
}

func unpcBlock(pc1, out []int32, num int, coefs []int16, numactive int, chanbits, denshift uint32) {
	chanshift := 32 - chanbits

	out[0] = pc1[0]

	if numactive == 0 {
		copy(out[1:num], pc1[1:num])
		return
	}

	if numactive == 31 {
		// Plain first-order delta. Used for the "shift" pass when the frame
		// carries extra low-order bits.
		prev := out[0]
		for j := 1; j < num; j++ {
			del := pc1[j] + prev
			prev = (del << chanshift) >> chanshift
			out[j] = prev
		}
		return
	}

	for j := 1; j <= numactive && j < num; j++ {
		del := pc1[j] + out[j-1]
		out[j] = (del << chanshift) >> chanshift
	}

	lim := numactive + 1
	denhalf := int32(1) << (denshift - 1)

	for j := lim; j < num; j++ {
		top := out[j-lim]
		// pout[-k] in the reference is out[j-1-k].
		var sum1 int32
		for k := 0; k < numactive; k++ {
			sum1 += int32(coefs[k]) * (out[j-1-k] - top)
		}

		del := pc1[j]
		del0 := del
		sg := signOfInt(del)
		del += top + ((sum1 + denhalf) >> denshift)
		out[j] = (del << chanshift) >> chanshift

		switch {
		case sg > 0:
			for k := numactive - 1; k >= 0; k-- {
				dd := top - out[j-1-k]
				sgn := signOfInt(dd)
				coefs[k] -= int16(sgn)
				del0 -= int32(numactive-k) * ((sgn * dd) >> denshift)
				if del0 <= 0 {
					break
				}
			}
		case sg < 0:
			for k := numactive - 1; k >= 0; k-- {
				dd := top - out[j-1-k]
				sgn := signOfInt(dd)
				coefs[k] += int16(sgn)
				del0 -= int32(numactive-k) * ((-sgn * dd) >> denshift)
				if del0 >= 0 {
					break
				}
			}
		}
	}
}
