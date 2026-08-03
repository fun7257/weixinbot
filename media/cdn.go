package media

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// CDN helps build URLs and upload/download encrypted blobs.
type CDN struct {
	BaseURL string
	HTTP    interface {
		Do(*http.Request) (*http.Response, error)
	}
	// MaxDownloadBytes limits DownloadCiphertext body size (0 → DefaultMaxBytes / DefaultDownloadMaxBytes).
	MaxDownloadBytes int64
	// AllowAnyFullURL disables host pinning for upload_full_url (tests only).
	// Inbound full_url from the server is always used when it passes soft SSRF checks
	// (matches openclaw-weixin, which prefers full_url without CDN-base pin).
	AllowAnyFullURL bool
}

// BuildDownloadURL joins encrypt_query_param onto the CDN base.
func BuildDownloadURL(cdnBaseURL, encryptQueryParam string) string {
	base := strings.TrimRight(cdnBaseURL, "/")
	return base + "/download?encrypted_query_param=" + url.QueryEscape(encryptQueryParam)
}

// BuildUploadURL joins upload_param and filekey onto the CDN base.
func BuildUploadURL(cdnBaseURL, uploadParam, filekey string) string {
	base := strings.TrimRight(cdnBaseURL, "/")
	return base + "/upload?encrypted_query_param=" + url.QueryEscape(uploadParam) +
		"&filekey=" + url.QueryEscape(filekey)
}

// CDNUploadMaxRetries matches openclaw-weixin cdn-upload (client errors do not retry).
const CDNUploadMaxRetries = 3

// UploadCiphertext POSTs AES ciphertext to the CDN and returns x-encrypted-param.
// Retries up to CDNUploadMaxRetries times on network/5xx/missing header; 4xx aborts.
func (c *CDN) UploadCiphertext(ctx context.Context, uploadFullURL, uploadParam, filekey string, ciphertext []byte) (downloadParam string, err error) {
	var target string
	if strings.TrimSpace(uploadFullURL) != "" {
		target = strings.TrimSpace(uploadFullURL)
		if !c.AllowAnyFullURL {
			if err := validateCDNFullURL(target, c.BaseURL); err != nil {
				// Prefer building from upload_param when full URL is untrusted.
				if uploadParam == "" {
					return "", fmt.Errorf("media: upload_full_url rejected: %w", err)
				}
				target = BuildUploadURL(c.BaseURL, uploadParam, filekey)
			}
		}
	} else if uploadParam != "" {
		target = BuildUploadURL(c.BaseURL, uploadParam, filekey)
	} else {
		return "", fmt.Errorf("media: CDN upload URL missing (need upload_full_url or upload_param)")
	}

	client := c.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	max := c.maxBytes()
	var last error
	for attempt := 1; attempt <= CDNUploadMaxRetries; attempt++ {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(ciphertext))
		if err != nil {
			return "", err
		}
		req.Header.Set("Content-Type", "application/octet-stream")

		res, err := client.Do(req)
		if err != nil {
			last = fmt.Errorf("media: cdn upload: %w", err)
			continue
		}
		body, rerr := io.ReadAll(io.LimitReader(res.Body, max+1))
		_ = res.Body.Close()
		if rerr != nil {
			last = rerr
			continue
		}
		if int64(len(body)) > max {
			return "", fmt.Errorf("media: cdn upload response exceeds MaxDownloadBytes %d", max)
		}
		if res.StatusCode >= 400 && res.StatusCode < 500 {
			// Client errors are not retried (same as openclaw-weixin).
			return "", fmt.Errorf("media: cdn client error %d: %s", res.StatusCode, headerOrBody(res, body))
		}
		if res.StatusCode != http.StatusOK {
			last = fmt.Errorf("media: cdn server error %d: %s", res.StatusCode, headerOrBody(res, body))
			continue
		}
		param := res.Header.Get("x-encrypted-param")
		if param == "" {
			param = res.Header.Get("X-Encrypted-Param")
		}
		if param == "" {
			last = fmt.Errorf("media: CDN response missing x-encrypted-param header")
			continue
		}
		return param, nil
	}
	if last == nil {
		last = fmt.Errorf("media: cdn upload failed after %d attempts", CDNUploadMaxRetries)
	}
	return "", last
}

// DownloadCiphertext GETs a CDN object.
//
// Production rule (openclaw-weixin downloadAndDecryptBuffer):
//  1. If full_url is present → use it (server-issued; only soft SSRF filter)
//  2. Else build from encrypt_query_param + CDN base
//
// Do NOT pin full_url to the configured CDN base host — Tencent issues
// regional/short-lived hosts that are valid but not always *.cdn.weixin.qq.com.
func (c *CDN) DownloadCiphertext(ctx context.Context, fullURL, encryptQueryParam string) ([]byte, error) {
	target, err := c.resolveDownloadURL(fullURL, encryptQueryParam)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	client := c.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("media: cdn download: %w", err)
	}
	defer res.Body.Close()
	max := c.maxBytes()
	data, err := io.ReadAll(io.LimitReader(res.Body, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > max {
		return nil, fmt.Errorf("media: cdn download exceeds MaxDownloadBytes %d", max)
	}
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("media: cdn download status %d: %s", res.StatusCode, string(data))
	}
	return data, nil
}

func (c *CDN) resolveDownloadURL(fullURL, encryptQueryParam string) (string, error) {
	full := strings.TrimSpace(fullURL)
	if full != "" {
		if err := validateInboundMediaURL(full); err != nil {
			// Soft fail: try encrypt_query_param fallback when full_url is unusable.
			if encryptQueryParam == "" {
				return "", err
			}
		} else {
			return full, nil
		}
	}
	if encryptQueryParam != "" {
		if c == nil || strings.TrimSpace(c.BaseURL) == "" {
			return "", fmt.Errorf("media: CDN BaseURL required to build download URL")
		}
		return BuildDownloadURL(c.BaseURL, encryptQueryParam), nil
	}
	return "", fmt.Errorf("media: download URL missing (need full_url or encrypt_query_param)")
}

func (c *CDN) maxBytes() int64 {
	if c != nil && c.MaxDownloadBytes > 0 {
		return c.MaxDownloadBytes
	}
	return DefaultDownloadMaxBytes
}

// validateInboundMediaURL is a soft SSRF filter for server-issued full_url.
// Unlike validateCDNFullURL (upload), it does NOT require Weixin CDN host pin.
func validateInboundMediaURL(full string) error {
	u, err := url.Parse(full)
	if err != nil {
		return fmt.Errorf("media: bad full_url: %w", err)
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "https" && scheme != "http" {
		return fmt.Errorf("media: full_url scheme %q not allowed", u.Scheme)
	}
	if u.User != nil {
		return fmt.Errorf("media: full_url must not contain userinfo")
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("media: full_url missing host")
	}
	if ip := net.ParseIP(host); ip != nil {
		if isPrivateIP(ip) {
			return fmt.Errorf("media: full_url host is private IP")
		}
	}
	return nil
}

// validateCDNFullURL requires https/http and host matching CDN base or Weixin CDN.
// Used for upload_full_url (client-chosen / server-issued upload endpoint).
func validateCDNFullURL(full, cdnBase string) error {
	if err := validateInboundMediaURL(full); err != nil {
		return err
	}
	u, _ := url.Parse(full)
	host := strings.ToLower(u.Hostname())
	if cdnBase != "" {
		bu, err := url.Parse(cdnBase)
		if err == nil && bu.Hostname() != "" {
			bh := strings.ToLower(bu.Hostname())
			if host == bh || strings.HasSuffix(host, "."+bh) || strings.HasSuffix(bh, "."+host) {
				return nil
			}
			if isWeixinHost(host) {
				return nil
			}
			return fmt.Errorf("media: full_url host %q not allowed for CDN base %q", host, bh)
		}
	}
	if isWeixinHost(host) {
		return nil
	}
	return fmt.Errorf("media: full_url host %q not allowlisted", host)
}

func isWeixinHost(host string) bool {
	return host == "weixin.qq.com" ||
		strings.HasSuffix(host, ".weixin.qq.com") ||
		host == "qq.com" ||
		strings.HasSuffix(host, ".qq.com")
}

func isPrivateIP(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsPrivate() || ip.IsUnspecified() {
		return true
	}
	// Cloud metadata
	if ip4 := ip.To4(); ip4 != nil {
		if ip4[0] == 169 && ip4[1] == 254 {
			return true
		}
	}
	return false
}

func headerOrBody(res *http.Response, body []byte) string {
	if m := res.Header.Get("x-error-message"); m != "" {
		return m
	}
	return string(body)
}
