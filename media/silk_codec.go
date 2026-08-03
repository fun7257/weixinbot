//go:build !race

package media

import (
	"fmt"

	gosilk "github.com/wdvxdr1123/go-silk"
)

// RealSilkCodec implements SilkCodec using the pure-Go (ccgo) SKP Silk port
// from github.com/wdvxdr1123/go-silk — no CGO required.
// Encodes Tencent-style SILK (0x02 + #!SILK_V3 header) for WeChat voice.
//
// Note: go-silk uses unsafe/ccgo and is incompatible with the race detector's
// checkptr; under -race, DefaultSilkCodec switches to FramedPCMSilkCodec.
type RealSilkCodec struct {
	// BitRate for encode; default 24000.
	BitRate int
}

// DecodeSilkToPCM decodes WeChat/Tencent SILK to mono s16le PCM.
func (c RealSilkCodec) DecodeSilkToPCM(silk []byte) (pcm []byte, sampleRate int, err error) {
	sr := SILKSampleRate
	pcm, err = gosilk.DecodeSilkBuffToPcm(silk, sr)
	if err != nil {
		return nil, 0, fmt.Errorf("media: silk decode: %w", err)
	}
	if len(pcm) == 0 {
		return nil, 0, fmt.Errorf("media: silk decode produced empty pcm")
	}
	return pcm, sr, nil
}

// EncodePCMToSilk encodes mono s16le PCM to Tencent SILK.
func (c RealSilkCodec) EncodePCMToSilk(pcm []byte, sampleRate int) (silk []byte, err error) {
	if sampleRate <= 0 {
		sampleRate = SILKSampleRate
	}
	br := c.BitRate
	if br <= 0 {
		br = 24000
	}
	minBytes := sampleRate / 1000 * 20 * 2
	if len(pcm) < minBytes {
		return nil, fmt.Errorf("media: pcm too short for silk encode (%d < %d)", len(pcm), minBytes)
	}
	out, err := gosilk.EncodePcmBuffToSilk(pcm, sampleRate, br, true /* tencent header */)
	if err != nil {
		return nil, fmt.Errorf("media: silk encode: %w", err)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("media: silk encode produced empty output")
	}
	return out, nil
}

func init() {
	DefaultSilkCodec = RealSilkCodec{}
}
