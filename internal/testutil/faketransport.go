// Package testutil provides shared fake HTTP helpers for weixinbot tests.
// Production code must never import this package.
package testutil

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
)

// Captured is one recorded HTTP request.
type Captured struct {
	Method string
	URL    string
	Header http.Header
	Body   []byte
}

// Path returns the URL path of the request.
func (c Captured) Path() string {
	if i := strings.Index(c.URL, "://"); i >= 0 {
		rest := c.URL[i+3:]
		if j := strings.Index(rest, "/"); j >= 0 {
			p := rest[j:]
			if k := strings.IndexAny(p, "?#"); k >= 0 {
				return p[:k]
			}
			return p
		}
	}
	// relative or bare path
	if k := strings.IndexAny(c.URL, "?#"); k >= 0 {
		return c.URL[:k]
	}
	return c.URL
}

// ResponseFunc builds a response for a request.
type ResponseFunc func(req *http.Request, body []byte) (*http.Response, error)

// FakeTransport routes by path suffix/substring queues and records all requests.
type FakeTransport struct {
	mu sync.Mutex

	// exact path suffix (e.g. "ilink/bot/getupdates") → FIFO of responses
	queues map[string][]ResponseFunc

	// fallback handlers matched by Contains on path
	contains []struct {
		sub string
		fn  ResponseFunc
	}

	// default when nothing matches
	Default ResponseFunc

	Captured []Captured
}

// NewFakeTransport returns an empty fake transport.
func NewFakeTransport() *FakeTransport {
	return &FakeTransport{
		queues: make(map[string][]ResponseFunc),
	}
}

// Enqueue appends scripted responses for a path key (matched via path contains).
func (f *FakeTransport) Enqueue(pathKey string, fns ...ResponseFunc) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.queues[pathKey] = append(f.queues[pathKey], fns...)
}

// EnqueueJSON queues a fixed status + JSON body for pathKey.
func (f *FakeTransport) EnqueueJSON(pathKey string, status int, v any) {
	f.Enqueue(pathKey, JSONResponder(status, v, nil))
}

// OnContains registers a sticky handler for paths containing sub.
func (f *FakeTransport) OnContains(sub string, fn ResponseFunc) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.contains = append(f.contains, struct {
		sub string
		fn  ResponseFunc
	}{sub: sub, fn: fn})
}

// RoundTrip implements http.RoundTripper.
func (f *FakeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	var body []byte
	if req.Body != nil {
		var err error
		body, err = io.ReadAll(req.Body)
		_ = req.Body.Close()
		if err != nil {
			return nil, err
		}
	}

	f.mu.Lock()
	f.Captured = append(f.Captured, Captured{
		Method: req.Method,
		URL:    req.URL.String(),
		Header: req.Header.Clone(),
		Body:   append([]byte(nil), body...),
	})

	path := req.URL.Path
	// Prefer queue match: first key contained in path with remaining items.
	for key, q := range f.queues {
		if strings.Contains(path, key) && len(q) > 0 {
			fn := q[0]
			f.queues[key] = q[1:]
			f.mu.Unlock()
			return fn(req, body)
		}
	}
	for _, c := range f.contains {
		if strings.Contains(path, c.sub) {
			fn := c.fn
			f.mu.Unlock()
			return fn(req, body)
		}
	}
	def := f.Default
	f.mu.Unlock()
	if def != nil {
		return def(req, body)
	}
	return JSONResponder(http.StatusNotFound, map[string]string{"error": "unknown path " + path}, nil)(req, body)
}

// Snapshot returns a copy of captured requests.
func (f *FakeTransport) Snapshot() []Captured {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]Captured, len(f.Captured))
	copy(out, f.Captured)
	return out
}

// CountPath returns how many captured requests contain pathSub.
func (f *FakeTransport) CountPath(pathSub string) int {
	n := 0
	for _, c := range f.Snapshot() {
		if strings.Contains(c.URL, pathSub) {
			n++
		}
	}
	return n
}

// Client returns an *http.Client using this transport.
func (f *FakeTransport) Client() *http.Client {
	return &http.Client{Transport: f}
}

// JSONResponder returns a ResponseFunc that writes status and JSON v.
func JSONResponder(status int, v any, extraHeader map[string]string) ResponseFunc {
	return func(req *http.Request, body []byte) (*http.Response, error) {
		var b []byte
		if v != nil {
			var err error
			b, err = json.Marshal(v)
			if err != nil {
				return nil, err
			}
		}
		h := make(http.Header)
		h.Set("Content-Type", "application/json")
		for k, val := range extraHeader {
			h.Set(k, val)
		}
		return &http.Response{
			StatusCode: status,
			Body:       io.NopCloser(bytes.NewReader(b)),
			Header:     h,
			Request:    req,
		}, nil
	}
}

// BytesResponder returns fixed bytes with optional headers.
func BytesResponder(status int, data []byte, hdr map[string]string) ResponseFunc {
	return func(req *http.Request, body []byte) (*http.Response, error) {
		h := make(http.Header)
		for k, v := range hdr {
			h.Set(k, v)
		}
		return &http.Response{
			StatusCode: status,
			Body:       io.NopCloser(bytes.NewReader(data)),
			Header:     h,
			Request:    req,
		}, nil
	}
}

// AssertBotAuth checks required iLink bot CGI auth headers.
func AssertBotAuth(h http.Header) error {
	if h.Get("AuthorizationType") != "ilink_bot_token" {
		return fmt.Errorf("AuthorizationType: got %q want ilink_bot_token", h.Get("AuthorizationType"))
	}
	auth := h.Get("Authorization")
	if !strings.HasPrefix(auth, "Bearer ") || len(auth) <= len("Bearer ") {
		return fmt.Errorf("Authorization: got %q want Bearer <token>", auth)
	}
	if h.Get("X-WECHAT-UIN") == "" {
		return fmt.Errorf("missing X-WECHAT-UIN")
	}
	return nil
}

// DecodeJSON unmarshals body into dst.
func DecodeJSON(body []byte, dst any) error {
	return json.Unmarshal(body, dst)
}

// MustJSON is like DecodeJSON but panics on error.
// Panic is intentional for test helpers only; production packages must not use this.
func MustJSON(body []byte, dst any) {
	if err := json.Unmarshal(body, dst); err != nil {
		panic(err)
	}
}
