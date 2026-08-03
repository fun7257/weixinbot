package media

import (
	"context"
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"

	"github.com/tencent-weixin/weixinbot/ilink"
	"github.com/tencent-weixin/weixinbot/protocol"
)

// Uploaded is the result of encrypting and uploading a local file to the CDN.
type Uploaded struct {
	FileKey                     string
	DownloadEncryptedQueryParam string
	AESKeyHex                   string
	// FileMD5 is hex MD5 of plaintext (for file_item.md5 / diagnostics).
	FileMD5            string
	FileSize           int
	FileSizeCiphertext int
}

// UploadFile reads path, requests getuploadurl, encrypts with AES-128-ECB, and POSTs to CDN.
func UploadFile(ctx context.Context, client *ilink.Client, cdn *CDN, filePath, toUserID string, mediaType int) (*Uploaded, error) {
	plaintext, err := os.ReadFile(filePath)
	if err != nil {
		return nil, fmt.Errorf("media: read file: %w", err)
	}
	rawsize := len(plaintext)
	sum := md5.Sum(plaintext)
	rawMD5 := hex.EncodeToString(sum[:])
	filesize := PaddedSize(rawsize)

	filekey := make([]byte, 16)
	if _, err := rand.Read(filekey); err != nil {
		return nil, err
	}
	aeskey := make([]byte, 16)
	if _, err := rand.Read(aeskey); err != nil {
		return nil, err
	}
	filekeyHex := hex.EncodeToString(filekey)
	aeskeyHex := hex.EncodeToString(aeskey)

	upResp, err := client.GetUploadURL(ctx, &protocol.GetUploadURLReq{
		FileKey:     filekeyHex,
		MediaType:   mediaType,
		ToUserID:    toUserID,
		RawSize:     rawsize,
		RawFileMD5:  rawMD5,
		FileSize:    filesize,
		NoNeedThumb: true,
		AESKey:      aeskeyHex,
	})
	if err != nil {
		return nil, err
	}
	if upResp.UploadFullURL == "" && upResp.UploadParam == "" {
		return nil, fmt.Errorf("media: getUploadUrl returned no upload URL")
	}

	ciphertext, err := EncryptAES128ECB(plaintext, aeskey)
	if err != nil {
		return nil, err
	}
	if len(ciphertext) != filesize {
		// still proceed; PaddedSize should match encrypt length
	}

	downloadParam, err := cdn.UploadCiphertext(ctx, upResp.UploadFullURL, upResp.UploadParam, filekeyHex, ciphertext)
	if err != nil {
		return nil, err
	}

	return &Uploaded{
		FileKey:                     filekeyHex,
		DownloadEncryptedQueryParam: downloadParam,
		AESKeyHex:                   aeskeyHex,
		FileMD5:                     rawMD5,
		FileSize:                    rawsize,
		FileSizeCiphertext:          filesize,
	}, nil
}

// DownloadAndDecrypt fetches CDN media and decrypts with a 16-byte raw key.
func DownloadAndDecrypt(ctx context.Context, cdn *CDN, fullURL, encryptQueryParam string, aesKey []byte) ([]byte, error) {
	ct, err := cdn.DownloadCiphertext(ctx, fullURL, encryptQueryParam)
	if err != nil {
		return nil, err
	}
	return DecryptAES128ECB(ct, aesKey)
}
