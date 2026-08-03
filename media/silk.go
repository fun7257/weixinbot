package media

import (
	"bytes"
	"encoding/binary"
	"fmt"
)

// SILKSampleRate is the WeChat voice sample rate (Hz).
const SILKSampleRate = 24000

// SilkHeader is the standard SILK V3 magic.
var SilkHeader = []byte("#!SILK_V3")

// IsSilk reports whether buf looks like a SILK bitstream (with optional 0x02 prefix).
func IsSilk(buf []byte) bool {
	if len(buf) >= 9 && bytes.Equal(buf[:9], SilkHeader) {
		return true
	}
	if len(buf) >= 10 && buf[0] == 0x02 && bytes.Equal(buf[1:10], SilkHeader) {
		return true
	}
	return false
}

// PCMToWAV wraps mono s16le PCM in a WAV container.
func PCMToWAV(pcm []byte, sampleRate int) []byte {
	if sampleRate <= 0 {
		sampleRate = SILKSampleRate
	}
	total := 44 + len(pcm)
	buf := make([]byte, total)
	copy(buf[0:4], "RIFF")
	binary.LittleEndian.PutUint32(buf[4:8], uint32(total-8))
	copy(buf[8:12], "WAVE")
	copy(buf[12:16], "fmt ")
	binary.LittleEndian.PutUint32(buf[16:20], 16) // pcm fmt chunk
	binary.LittleEndian.PutUint16(buf[20:22], 1)  // PCM
	binary.LittleEndian.PutUint16(buf[22:24], 1)  // mono
	binary.LittleEndian.PutUint32(buf[24:28], uint32(sampleRate))
	binary.LittleEndian.PutUint32(buf[28:32], uint32(sampleRate*2))
	binary.LittleEndian.PutUint16(buf[32:34], 2)
	binary.LittleEndian.PutUint16(buf[34:36], 16)
	copy(buf[36:40], "data")
	binary.LittleEndian.PutUint32(buf[40:44], uint32(len(pcm)))
	copy(buf[44:], pcm)
	return buf
}

// WAVToPCM extracts s16le mono PCM from a simple WAV buffer.
func WAVToPCM(wav []byte) (pcm []byte, sampleRate int, err error) {
	if len(wav) < 44 || string(wav[0:4]) != "RIFF" || string(wav[8:12]) != "WAVE" {
		return nil, 0, fmt.Errorf("media: not a WAV file")
	}
	// naive scan for "fmt " and "data"
	i := 12
	var sr int
	for i+8 <= len(wav) {
		chunk := string(wav[i : i+4])
		size := int(binary.LittleEndian.Uint32(wav[i+4 : i+8]))
		i += 8
		if i+size > len(wav) {
			break
		}
		if chunk == "fmt " && size >= 16 {
			sr = int(binary.LittleEndian.Uint32(wav[i+4 : i+8]))
		}
		if chunk == "data" {
			return append([]byte(nil), wav[i:i+size]...), sr, nil
		}
		i += size
		if size%2 == 1 {
			i++
		}
	}
	return nil, 0, fmt.Errorf("media: WAV missing data chunk")
}

// SilkCodec encodes/decodes SILK. Implementations may use CGO libraries.
// The default NilSilkCodec always fails so callers degrade to raw silk / explicit errors.
type SilkCodec interface {
	// DecodeSilkToPCM returns mono s16le PCM and sample rate.
	DecodeSilkToPCM(silk []byte) (pcm []byte, sampleRate int, err error)
	// EncodePCMToSilk encodes mono s16le PCM at sampleRate to SILK.
	EncodePCMToSilk(pcm []byte, sampleRate int) (silk []byte, err error)
}

// NilSilkCodec always returns errors (degrade path).
type NilSilkCodec struct{}

func (NilSilkCodec) DecodeSilkToPCM([]byte) ([]byte, int, error) {
	return nil, 0, fmt.Errorf("media: silk decoder not available")
}
func (NilSilkCodec) EncodePCMToSilk([]byte, int) ([]byte, error) {
	return nil, fmt.Errorf("media: silk encoder not available")
}

// DefaultSilkCodec is used when Session/DownloadDeps do not inject one.
// Initialized to RealSilkCodec in silk_codec.go (pure-Go SKP Silk via go-silk).
// Tests may replace with NilSilkCodec to exercise degrade paths.
var DefaultSilkCodec SilkCodec = NilSilkCodec{}

// SilkToWAV tries codec decode then PCMToWAV; returns error on failure.
func SilkToWAV(silk []byte, codec SilkCodec) ([]byte, error) {
	if codec == nil {
		codec = DefaultSilkCodec
	}
	pcm, sr, err := codec.DecodeSilkToPCM(silk)
	if err != nil {
		return nil, err
	}
	if sr <= 0 {
		sr = SILKSampleRate
	}
	return PCMToWAV(pcm, sr), nil
}

// WAVOrPCMToSilk encodes WAV or raw PCM; if input is already SILK, returns it.
func WAVOrPCMToSilk(data []byte, codec SilkCodec) ([]byte, error) {
	if IsSilk(data) {
		return data, nil
	}
	if codec == nil {
		codec = DefaultSilkCodec
	}
	if pcm, sr, err := WAVToPCM(data); err == nil {
		if sr <= 0 {
			sr = SILKSampleRate
		}
		return codec.EncodePCMToSilk(pcm, sr)
	}
	// treat as raw PCM at default rate
	return codec.EncodePCMToSilk(data, SILKSampleRate)
}
