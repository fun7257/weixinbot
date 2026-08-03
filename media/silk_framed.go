package media

import (
	"encoding/binary"
	"fmt"
)

// FramedPCMSilkCodec is a pure-Go SILK-V3 *framed* codec used for:
//   - race-detector builds (go-silk/ccgo is checkptr-incompatible)
//   - deterministic unit tests without native/ccgo code
//
// Wire layout (compatible enough for our own encode→decode path):
//
//	#!SILK_V3 | repeated (uint16le frameLen + pcmFrameBytes)
//
// This is NOT SKP Silk compression. Production non-race builds use RealSilkCodec
// (github.com/wdvxdr1123/go-silk) for WeChat-compatible bitstreams.
type FramedPCMSilkCodec struct {
	// FrameMS frame duration; default 20.
	FrameMS int
}

// DecodeSilkToPCM concatenates framed PCM after the SILK_V3 header.
func (c FramedPCMSilkCodec) DecodeSilkToPCM(silk []byte) (pcm []byte, sampleRate int, err error) {
	body := silk
	if len(body) >= 10 && body[0] == 0x02 && string(body[1:10]) == "#!SILK_V3" {
		body = body[10:]
	} else if len(body) >= 9 && string(body[:9]) == "#!SILK_V3" {
		body = body[9:]
	} else {
		return nil, 0, fmt.Errorf("media: framed silk: bad header")
	}
	// optional sample rate prefix: 0xFFFF marker + uint32 le rate
	sr := SILKSampleRate
	if len(body) >= 6 && body[0] == 0xff && body[1] == 0xff {
		sr = int(binary.LittleEndian.Uint32(body[2:6]))
		body = body[6:]
	}
	var out []byte
	for len(body) >= 2 {
		n := int(binary.LittleEndian.Uint16(body[:2]))
		body = body[2:]
		if n <= 0 || n > len(body) {
			break
		}
		out = append(out, body[:n]...)
		body = body[n:]
	}
	if len(out) == 0 {
		return nil, 0, fmt.Errorf("media: framed silk: empty pcm")
	}
	return out, sr, nil
}

// EncodePCMToSilk wraps PCM in SILK_V3 framed layout.
func (c FramedPCMSilkCodec) EncodePCMToSilk(pcm []byte, sampleRate int) ([]byte, error) {
	if sampleRate <= 0 {
		sampleRate = SILKSampleRate
	}
	if len(pcm) < 2 {
		return nil, fmt.Errorf("media: framed silk: pcm too short")
	}
	ms := c.FrameMS
	if ms <= 0 {
		ms = 20
	}
	frameBytes := sampleRate / 1000 * ms * 2 // s16le mono
	if frameBytes < 2 {
		frameBytes = 2
	}
	out := make([]byte, 0, 16+len(pcm)+len(pcm)/frameBytes*2)
	out = append(out, 0x02)
	out = append(out, []byte("#!SILK_V3")...)
	// sample rate marker
	out = append(out, 0xff, 0xff)
	var srBuf [4]byte
	binary.LittleEndian.PutUint32(srBuf[:], uint32(sampleRate))
	out = append(out, srBuf[:]...)
	for i := 0; i < len(pcm); {
		n := frameBytes
		if i+n > len(pcm) {
			n = len(pcm) - i
		}
		var lb [2]byte
		binary.LittleEndian.PutUint16(lb[:], uint16(n))
		out = append(out, lb[:]...)
		out = append(out, pcm[i:i+n]...)
		i += n
	}
	return out, nil
}
