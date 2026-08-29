// Differential test-vector generator for the Go ALAC port.
//
// Encodes a set of deliberately awkward PCM frames with Apple's reference
// encoder at the exact parameters AirPlay negotiates (352 frames, 16-bit,
// stereo, 44100), decodes them again with Apple's reference decoder, and
// writes both the compressed frames and the reference PCM to disk.
//
// The Go test then decodes the same frames and must produce byte-identical
// PCM. Comparing against the reference rather than against "does it sound
// right" is the point: a Rice decoder that is subtly wrong still emits
// plausible numbers.
#include "ALACEncoder.h"
#include "ALACDecoder.h"
#include "ALACBitUtilities.h"
#include "ALACAudioTypes.h"

#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <math.h>

static const int kFrames   = 352;   // AirPlay's a=fmtp frame length
static const int kChannels = 2;
static const int kBits     = 16;

// Signals chosen to exercise different code paths: silence and DC hit the
// zero-run branch, noise defeats the predictor and forces long Rice codes,
// full-scale and the impulse train probe clipping and sign handling, and the
// correlated/anti-correlated pairs drive the mid/side mixRes selection.
static void fill(int which, int frameIdx, int16_t *out)
{
    static unsigned int seed = 12345;
    for (int i = 0; i < kFrames; i++) {
        double t = (double)(frameIdx * kFrames + i);
        int32_t l = 0, r = 0;
        switch (which) {
        case 0: l = 0; r = 0; break;                                  // silence
        case 1: l = 1000; r = 1000; break;                            // DC
        case 2:                                                        // sine, correlated
            l = (int32_t)(20000.0 * sin(2.0 * M_PI * 440.0 * t / 44100.0));
            r = l;
            break;
        case 3:                                                        // sine, anti-correlated
            l = (int32_t)(20000.0 * sin(2.0 * M_PI * 440.0 * t / 44100.0));
            r = -l;
            break;
        case 4:                                                        // white noise
            seed = seed * 1103515245u + 12345u;
            l = (int16_t)(seed >> 16);
            seed = seed * 1103515245u + 12345u;
            r = (int16_t)(seed >> 16);
            break;
        case 5:                                                        // full scale square
            l = ((i / 16) % 2) ? 32767 : -32768;
            r = -l;
            break;
        case 6:                                                        // impulse train
            l = (i % 37 == 0) ? 32767 : 0;
            r = (i % 53 == 0) ? -32768 : 0;
            break;
        case 7:                                                        // quiet dither
            seed = seed * 1103515245u + 12345u;
            l = (int16_t)((seed >> 16) & 0x3) - 2;
            seed = seed * 1103515245u + 12345u;
            r = (int16_t)((seed >> 16) & 0x3) - 2;
            break;
        }
        out[i * 2 + 0] = (int16_t)l;
        out[i * 2 + 1] = (int16_t)r;
    }
}

int main(int argc, char **argv)
{
    const char *framesPath = argv[1];
    const char *pcmPath    = argv[2];

    AudioFormatDescription inFmt, outFmt;
    memset(&inFmt, 0, sizeof(inFmt));
    memset(&outFmt, 0, sizeof(outFmt));

    inFmt.mSampleRate       = 44100;
    inFmt.mFormatID         = kALACFormatLinearPCM;
    inFmt.mFormatFlags      = kALACFormatFlagIsSignedInteger | kALACFormatFlagIsPacked;
    inFmt.mBytesPerPacket   = kChannels * (kBits / 8);
    inFmt.mFramesPerPacket  = 1;
    inFmt.mBytesPerFrame    = kChannels * (kBits / 8);
    inFmt.mChannelsPerFrame = kChannels;
    inFmt.mBitsPerChannel   = kBits;

    outFmt.mSampleRate       = 44100;
    outFmt.mFormatID         = kALACFormatAppleLossless;
    outFmt.mFormatFlags      = 1;   // 16-bit source
    outFmt.mBytesPerPacket   = 0;
    outFmt.mFramesPerPacket  = kFrames;
    outFmt.mBytesPerFrame    = 0;
    outFmt.mChannelsPerFrame = kChannels;
    outFmt.mBitsPerChannel   = 0;

    ALACEncoder *enc = new ALACEncoder;
    enc->SetFrameSize(kFrames);
    enc->InitializeEncoder(outFmt);

    uint32_t cookieSize = enc->GetMagicCookieSize(kChannels);
    uint8_t *cookie = (uint8_t *)calloc(cookieSize, 1);
    enc->GetMagicCookie(cookie, &cookieSize);

    ALACDecoder *dec = new ALACDecoder;
    if (dec->Init(cookie, cookieSize) != 0) {
        fprintf(stderr, "decoder init failed\n");
        return 1;
    }

    FILE *ff = fopen(framesPath, "wb");
    FILE *fp = fopen(pcmPath, "wb");
    if (!ff || !fp) { fprintf(stderr, "cannot open outputs\n"); return 1; }

    int16_t pcm[kFrames * kChannels];
    uint8_t packet[kFrames * kChannels * 4 + 1024];
    uint8_t decoded[kFrames * kChannels * 4 + 1024];

    int nCases = 8, nFramesPer = 4, total = 0;
    for (int which = 0; which < nCases; which++) {
        for (int f = 0; f < nFramesPer; f++) {
            fill(which, f, pcm);

            int32_t numBytes = kFrames * kChannels * (kBits / 8);
            if (enc->Encode(inFmt, outFmt, (uint8_t *)pcm, packet, &numBytes) != 0) {
                fprintf(stderr, "encode failed (case %d frame %d)\n", which, f);
                return 1;
            }

            BitBuffer bits;
            BitBufferInit(&bits, packet, numBytes);
            uint32_t outSamples = 0;
            if (dec->Decode(&bits, decoded, kFrames, kChannels, &outSamples) != 0) {
                fprintf(stderr, "reference decode failed (case %d frame %d)\n", which, f);
                return 1;
            }
            if ((int)outSamples != kFrames) {
                fprintf(stderr, "short decode: %u\n", outSamples);
                return 1;
            }

            // frames file: uint32 length, then the packet
            uint32_t len = (uint32_t)numBytes;
            fwrite(&len, 4, 1, ff);
            fwrite(packet, 1, numBytes, ff);

            fwrite(decoded, 1, kFrames * kChannels * (kBits / 8), fp);
            total++;
        }
    }

    fclose(ff);
    fclose(fp);
    fprintf(stderr, "wrote %d frames\n", total);
    return 0;
}
