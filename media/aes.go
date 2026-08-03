// Package media implements the CDN data plane: AES-128-ECB and upload/download helpers.
package media

import (
	"crypto/aes"
	"crypto/cipher"
	"fmt"
)

const blockSize = aes.BlockSize

// PaddedSize returns AES-128-ECB ciphertext length with PKCS7 padding.
func PaddedSize(plaintextSize int) int {
	return ((plaintextSize + blockSize) / blockSize) * blockSize
}

// EncryptAES128ECB encrypts plaintext with AES-128-ECB and PKCS7 padding.
// key must be 16 bytes.
func EncryptAES128ECB(plaintext, key []byte) ([]byte, error) {
	if len(key) != 16 {
		return nil, fmt.Errorf("media: aes key must be 16 bytes, got %d", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	padded := pkcs7Pad(plaintext, blockSize)
	out := make([]byte, len(padded))
	ecbEncrypt(block, out, padded)
	return out, nil
}

// DecryptAES128ECB decrypts AES-128-ECB PKCS7 ciphertext.
func DecryptAES128ECB(ciphertext, key []byte) ([]byte, error) {
	if len(key) != 16 {
		return nil, fmt.Errorf("media: aes key must be 16 bytes, got %d", len(key))
	}
	if len(ciphertext) == 0 || len(ciphertext)%blockSize != 0 {
		return nil, fmt.Errorf("media: ciphertext length %d not multiple of block", len(ciphertext))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	out := make([]byte, len(ciphertext))
	ecbDecrypt(block, out, ciphertext)
	return pkcs7Unpad(out, blockSize)
}

func pkcs7Pad(b []byte, size int) []byte {
	pad := size - len(b)%size
	out := make([]byte, len(b)+pad)
	copy(out, b)
	for i := len(b); i < len(out); i++ {
		out[i] = byte(pad)
	}
	return out
}

func pkcs7Unpad(b []byte, size int) ([]byte, error) {
	if len(b) == 0 || len(b)%size != 0 {
		return nil, fmt.Errorf("media: invalid padded length")
	}
	pad := int(b[len(b)-1])
	if pad == 0 || pad > size || pad > len(b) {
		return nil, fmt.Errorf("media: invalid pkcs7 pad %d", pad)
	}
	for i := 0; i < pad; i++ {
		if b[len(b)-1-i] != byte(pad) {
			return nil, fmt.Errorf("media: bad pkcs7 padding")
		}
	}
	return b[:len(b)-pad], nil
}

// ecbEncrypt encrypts src into dst (same length, multiple of block).
func ecbEncrypt(block cipher.Block, dst, src []byte) {
	bs := block.BlockSize()
	for i := 0; i < len(src); i += bs {
		block.Encrypt(dst[i:i+bs], src[i:i+bs])
	}
}

func ecbDecrypt(block cipher.Block, dst, src []byte) {
	bs := block.BlockSize()
	for i := 0; i < len(src); i += bs {
		block.Decrypt(dst[i:i+bs], src[i:i+bs])
	}
}
