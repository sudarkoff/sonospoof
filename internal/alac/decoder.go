package alac

import "errors"

// Element tags, from ALACAudioTypes.h.
const (
	idSCE = 0 // single channel element (mono)
	idCPE = 1 // channel pair element (stereo)
	idCCE = 2
	idLFE = 3
	idDSE = 4
	idPCE = 5
	idFIL = 6
	idEND = 7
)

var (
	errTruncated    = errors.New("alac: frame ended mid-symbol")
	errCorruptRun   = errors.New("alac: zero-run overruns the frame")
	errBadElement   = errors.New("alac: unsupported element type")
	errBadShift     = errors.New("alac: bytesShifted == 3 is reserved")
	errFrameTooLong = errors.New("alac: frame declares more samples than configured")
)

// Config is the ALAC magic cookie, which AirPlay delivers as the SDP
// a=fmtp:96 line. The field order there is exactly the order below, e.g.
//
//	a=fmtp:96 352 0 16 40 10 14 2 255 0 0 44100
type Config struct {
	FrameLength       uint32
	CompatibleVersion uint8
	BitDepth          uint8
	PB                uint8
	MB                uint8
	KB                uint8
	NumChannels       uint8
	MaxRun            uint16
	MaxFrameBytes     uint32
	AvgBitRate        uint32
	SampleRate        uint32
}

// Decoder holds the scratch buffers for one stream. It is not safe for
// concurrent use; one decoder belongs to one session.
type Decoder struct {
	cfg Config

	predictor  []int32
	mixBufferU []int32
	mixBufferV []int32
	shift      []uint16

	coefsU [32]int16
	coefsV [32]int16
}

func NewDecoder(cfg Config) *Decoder {
	n := int(cfg.FrameLength)
	return &Decoder{
		cfg:        cfg,
		predictor:  make([]int32, n),
		mixBufferU: make([]int32, n),
		mixBufferV: make([]int32, n),
		shift:      make([]uint16, n*2),
	}
}

// Decode decodes one ALAC frame into out as interleaved little-endian
// int16 samples, returning the number of sample frames written. out must
// hold FrameLength*NumChannels int16s.
//
// Only the 16-bit path is implemented: AirPlay 1 negotiates 16-bit stereo and
// nothing in this project produces anything else. A different bit depth is
// reported as an error rather than silently mis-decoded.
func (d *Decoder) Decode(frame []byte, out []int16) (int, error) {
	if d.cfg.BitDepth != 16 {
		return 0, errors.New("alac: only 16-bit output is implemented")
	}

	bits := newBitBuffer(frame)
	channelIndex := 0
	numSamples := 0

	for {
		tag := bits.read(3)

		switch tag {
		case idSCE, idCPE:
			pairs := 1
			if tag == idCPE {
				pairs = 2
			}
			n, err := d.decodeElement(bits, out, channelIndex, pairs)
			if err != nil {
				return 0, err
			}
			numSamples = n
			channelIndex += pairs

		case idFIL, idDSE:
			// Fill/data elements carry no audio. The reference parses them
			// properly; nothing in an AirPlay stream emits them, so treat
			// their presence as unexpected rather than pretending to skip.
			return 0, errBadElement

		case idCCE, idPCE, idLFE:
			return 0, errBadElement

		case idEND:
			bits.byteAlign()
			return numSamples, nil

		default:
			return 0, errBadElement
		}

		if bits.exhausted() {
			return 0, errTruncated
		}
	}
}

// decodeElement handles one SCE (chans==1) or CPE (chans==2).
func (d *Decoder) decodeElement(bits *bitBuffer, out []int16, channelIndex, chans int) (int, error) {
	_ = bits.read(4)  // elementInstanceTag
	_ = bits.read(12) // unused header

	headerByte := bits.read(4)
	partialFrame := headerByte >> 3
	bytesShifted := (headerByte >> 1) & 0x3
	escapeFlag := headerByte & 0x1

	if bytesShifted == 3 {
		return 0, errBadShift
	}

	// A stereo pair carries one extra bit of range because of the mid/side
	// transform; mono does not.
	chanBits := uint32(d.cfg.BitDepth) - bytesShifted*8
	if chans == 2 {
		chanBits++
	}

	numSamples := int(d.cfg.FrameLength)
	if partialFrame != 0 {
		numSamples = int(bits.read(16)<<16 | bits.read(16))
		if numSamples > int(d.cfg.FrameLength) {
			return 0, errFrameTooLong
		}
	}

	var mixBits, mixRes int32
	var shiftBits *bitBuffer

	if escapeFlag == 0 {
		mixBits = int32(bits.read(8))
		mixRes = int32(int8(bits.read(8))) // signed

		// Both channels' parameters are read before either is decoded, so
		// each channel's pbFactor must be carried alongside it rather than
		// held in one shared slot.
		modeU, denShiftU, numU, pbFactorU := d.readCoefs(bits, &d.coefsU)
		var modeV, denShiftV, pbFactorV uint32
		var numV int
		if chans == 2 {
			modeV, denShiftV, numV, pbFactorV = d.readCoefs(bits, &d.coefsV)
		}

		// The shifted low-order bits sit interleaved ahead of the residuals;
		// remember where they start and step over them.
		if bytesShifted != 0 {
			saved := *bits
			shiftBits = &saved
			bits.advance(int(bytesShifted) * 8 * chans * numSamples)
		}

		if err := d.channel(bits, d.mixBufferU, numSamples, chanBits,
			modeU, denShiftU, numU, d.coefsU[:], pbFactorU); err != nil {
			return 0, err
		}
		if chans == 2 {
			if err := d.channel(bits, d.mixBufferV, numSamples, chanBits,
				modeV, denShiftV, numV, d.coefsV[:], pbFactorV); err != nil {
				return 0, err
			}
		}
	} else {
		// Uncompressed frame: samples are stored raw.
		chanBits = uint32(d.cfg.BitDepth)
		sh := 32 - chanBits
		for i := 0; i < numSamples; i++ {
			v := int32(bits.read(chanBits))
			d.mixBufferU[i] = (v << sh) >> sh
			if chans == 2 {
				v = int32(bits.read(chanBits))
				d.mixBufferV[i] = (v << sh) >> sh
			}
		}
		mixBits, mixRes = 0, 0
		bytesShifted = 0
	}

	if bytesShifted != 0 && shiftBits != nil {
		sh := bytesShifted * 8
		for i := 0; i < numSamples*chans; i++ {
			d.shift[i] = uint16(shiftBits.read(sh))
		}
	}

	stride := int(d.cfg.NumChannels)
	if chans == 2 {
		unmix16(d.mixBufferU, d.mixBufferV, out[channelIndex:], stride, numSamples, mixBits, mixRes)
	} else {
		for i := 0; i < numSamples; i++ {
			out[channelIndex+i*stride] = int16(d.mixBufferU[i])
		}
	}
	return numSamples, nil
}

// readCoefs pulls one channel's predictor description.
func (d *Decoder) readCoefs(bits *bitBuffer, coefs *[32]int16) (mode, denShift uint32, num int, pbFactor uint32) {
	h := bits.read(8)
	mode = h >> 4
	denShift = h & 0xf

	h = bits.read(8)
	pbFactor = h >> 5
	num = int(h & 0x1f)
	for i := 0; i < num; i++ {
		coefs[i] = int16(bits.read(16))
	}
	return mode, denShift, num, pbFactor
}

// channel decodes residuals for one channel and runs the predictor.
func (d *Decoder) channel(bits *bitBuffer, dst []int32, numSamples int, chanBits,
	mode, denShift uint32, num int, coefs []int16, pbFactor uint32) error {

	var params agParams
	setAGParams(&params, uint32(d.cfg.MB), (uint32(d.cfg.PB)*pbFactor)/4, uint32(d.cfg.KB),
		uint32(numSamples), uint32(numSamples), uint32(d.cfg.MaxRun))

	if _, err := dynDecomp(&params, bits, d.predictor, numSamples, chanBits); err != nil {
		return err
	}

	if mode == 0 {
		unpcBlock(d.predictor, dst, numSamples, coefs, num, chanBits, denShift)
	} else {
		// The numactive==31 pass is done in place, then the real predictor.
		unpcBlock(d.predictor, d.predictor, numSamples, nil, 31, chanBits, 0)
		unpcBlock(d.predictor, dst, numSamples, coefs, num, chanBits, denShift)
	}
	return nil
}

// unmix16 reverses the mid/side transform, from matrix_dec.c.
func unmix16(u, v []int32, out []int16, stride, numSamples int, mixBits, mixRes int32) {
	if mixRes != 0 {
		for j := 0; j < numSamples; j++ {
			l := u[j] + v[j] - ((mixRes * v[j]) >> mixBits)
			r := l - v[j]
			out[j*stride] = int16(l)
			out[j*stride+1] = int16(r)
		}
		return
	}
	for j := 0; j < numSamples; j++ {
		out[j*stride] = int16(u[j])
		out[j*stride+1] = int16(v[j])
	}
}
