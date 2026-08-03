//go:build race

package media

// Under the race detector, the ccgo-based go-silk package trips checkptr.
// Use FramedPCMSilkCodec so encode/decode paths and session tests still run.

func init() {
	DefaultSilkCodec = FramedPCMSilkCodec{}
}
