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
	// AllowAnyFullURL disables full_url / upload_full_url host pin (tests only). Prefer false in production.
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

// UploadCiphertext POSTs AES ciphertext to the CDN and returns x-encrypted-param.
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

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(ciphertext))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/octet-stream")

	client := c.HTTP
	if client == nil {
		client = http.DefaultClient
	}
	res, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("media: cdn upload: %w", err)
	}
	defer res.Body.Close()
	max := c.maxBytes()
	body, err := io.ReadAll(io.LimitReader(res.Body, max+1))
	if err != nil {
		return "", err
	}
	if int64(len(body)) > max {
		return "", fmt.Errorf("media: cdn upload response exceeds MaxDownloadBytes %d", max)
	}
	if res.StatusCode >= 400 && res.StatusCode < 500 {
		return "", fmt.Errorf("media: cdn client error %d: %s", res.StatusCode, headerOrBody(res, body))
	}
	if res.StatusCode != http.StatusOK {
		return "", fmt.Errorf("media: cdn server error %d: %s", res.StatusCode, headerOrBody(res, body))
	}
	param := res.Header.Get("x-encrypted-param")
	if param == "" {
		return "", fmt.Errorf("media: CDN response missing x-encrypted-param header")
	}
	return param, nil
}

// DownloadCiphertext GETs a CDN object (fullURL preferred when host-safe, else build from encrypt param).
func (c *CDN) DownloadCiphertext(ctx context.Context, fullURL, encryptQueryParam string) ([]byte, error) {
	var target string
	if strings.TrimSpace(fullURL) != "" {
		target = strings.TrimSpace(fullURL)
		if !c.AllowAnyFullURL {
			if err := validateCDNFullURL(target, c.BaseURL); err != nil {
				// Fall back to building from encrypt_query_param when full_url is untrusted.
				if encryptQueryParam == "" {
					return nil, err
				}
				target = BuildDownloadURL(c.BaseURL, encryptQueryParam)
			}
		}
	} else if encryptQueryParam != "" {
		target = BuildDownloadURL(c.BaseURL, encryptQueryParam)
	} else {
		return nil, fmt.Errorf("media: download URL missing")
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

func (c *CDN) maxBytes() int64 {
	if c != nil && c.MaxDownloadBytes > 0 {
		return c.MaxDownloadBytes
	}
	return DefaultDownloadMaxBytes
}

// validateCDNFullURL requires https and host matching CDN base registrable domain or suffix.
func validateCDNFullURL(full, cdnBase string) error {
	u, err := url.Parse(full)
	if err != nil {
		return fmt.Errorf("media: bad full_url: %w", err)
	}
	if u.Scheme != "https" && u.Scheme != "http" {
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
	// Prefer same host as configured CDN base when available.
	if cdnBase != "" {
		bu, err := url.Parse(cdnBase)
		if err == nil && bu.Hostname() != "" {
			bh := strings.ToLower(bu.Hostname())
			h := strings.ToLower(host)
			if h == bh || strings.HasSuffix(h, "."+bh) || strings.HasSuffix(bh, "."+h) {
				return nil
			}
			// Also allow common weixin CDN hosts.
			if isWeixinHost(h) {
				return nil
			}
			return fmt.Errorf("media: full_url host %q not allowed for CDN base %q", host, bh)
		}
	}
	if isWeixinHost(strings.ToLower(host)) {
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
