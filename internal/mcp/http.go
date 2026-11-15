package mcp

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	maxHTTPBody   = 16 << 20
	maxHTTPHeader = 1 << 20
)

// StatusError carries safe HTTP status metadata.
type StatusError struct {
	Status    int
	Retryable bool
	Auth      bool
	Session   bool
}

func (e *StatusError) Error() string { return fmt.Sprintf("HTTP status %d", e.Status) }

type httpTransportConfig struct {
	server  Server
	headers map[string]string
	client  *http.Client
}

func (m *Manager) newHTTPTransport(server Server, conn *connection) sdk.Transport {
	if _, err := validateEndpoint(server.URL); err != nil {
		return &errorTransport{err: err}
	}
	base := &http.Transport{
		Proxy:                  http.ProxyFromEnvironment,
		DialContext:            (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSClientConfig:        &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: m.opts.roots},
		TLSHandshakeTimeout:    5 * time.Second,
		ResponseHeaderTimeout:  10 * time.Second,
		MaxResponseHeaderBytes: maxHTTPHeader,
		ForceAttemptHTTP2:      true,
	}
	headers := make(map[string]string, len(server.Headers))
	for name, value := range server.Headers {
		headers[name] = value
	}
	client := &http.Client{
		Transport: &headerRoundTripper{base: base, headers: headers, conn: conn},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("MCP HTTP redirects are not supported")
		},
	}
	return &sdk.StreamableClientTransport{
		Endpoint:     server.URL,
		HTTPClient:   client,
		MaxRetries:   -1,
		MaxEventSize: maxHTTPBody,
	}
}

func validateEndpoint(raw string) (*url.URL, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil, configErr("config", "MCP url must be an absolute http or https URL")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, configErr("config", "MCP url must use http or https")
	}
	if parsed.User != nil {
		return nil, configErr("config", "MCP url must not contain user information")
	}
	if parsed.Fragment != "" {
		return nil, configErr("config", "MCP url must not contain a fragment")
	}
	return parsed, nil
}

type errorTransport struct{ err error }

func (t *errorTransport) Connect(context.Context) (sdk.Connection, error) { return nil, t.err }

type headerRoundTripper struct {
	base    http.RoundTripper
	headers map[string]string
	conn    *connection
}

func (t *headerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL == nil {
		return nil, errors.New("MCP request has no URL")
	}
	clone := req.Clone(req.Context())
	clone.Header = req.Header.Clone()
	var stopConn func() bool
	var cancel context.CancelFunc
	if t.conn != nil {
		ctx, cancelFunc := context.WithCancel(clone.Context())
		cancel = cancelFunc
		stopConn = context.AfterFunc(t.conn.ctx, cancel)
		clone = clone.WithContext(ctx)
	}
	for name, value := range t.headers {
		if _, reserved := reservedHeaders[strings.ToLower(name)]; reserved {
			return nil, fmt.Errorf("MCP header %q is reserved", name)
		}
		clone.Header.Set(name, value)
	}
	if req.Method == http.MethodPost {
		clone.GetBody = nil
	}
	resp, err := t.base.RoundTrip(clone)
	if req.Method == http.MethodGet {
		t.conn.markListenerStarted()
	}
	if err != nil {
		if stopConn != nil {
			stopConn()
		}
		if cancel != nil {
			cancel()
		}
		t.classify(err)
		return nil, err
	}
	if resp.Body != nil && cancel != nil {
		resp.Body = newCancelBody(resp.Body, t.conn.ctx, stopConn, cancel)
	} else if cancel != nil {
		if stopConn != nil {
			stopConn()
		}
		cancel()
	}
	t.conn.recordRetryAfter(resp.Header.Values("Retry-After"))
	lower := strings.ToLower(resp.Header.Get("Content-Type"))
	isSSE := strings.HasPrefix(lower, "text/event-stream")
	if !isSSE {
		resp.Body = &boundedReadCloser{body: resp.Body, remaining: maxHTTPBody, onOverflow: func() { t.conn.latchClass("limit") }}
	}
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		t.conn.latchClass("auth")
	case resp.StatusCode == http.StatusNotFound && req.Method == http.MethodPost && req.Header.Get("Mcp-Session-Id") != "":
		t.conn.latchClass("session-lost")
	case req.Method == http.MethodGet && resp.StatusCode == http.StatusTooManyRequests:
		t.conn.latchClass("rate-limit")
	case req.Method == http.MethodGet && resp.StatusCode >= 500:
		t.conn.latchClass("server")
	case resp.StatusCode == http.StatusRequestTimeout || resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
		t.conn.transient.Store(true)
	}
	return resp, nil
}

func (t *headerRoundTripper) classify(err error) {
	var unknown x509.UnknownAuthorityError
	var hostname x509.HostnameError
	var invalid x509.CertificateInvalidError
	if errors.As(err, &unknown) || errors.As(err, &hostname) || errors.As(err, &invalid) {
		t.conn.latchClass("certificate")
	}
}

var reservedHeaders = map[string]bool{
	"host": true, "content-length": true, "transfer-encoding": true,
	"content-type": true, "accept": true, "mcp-session-id": true,
	"mcp-protocol-version": true, "last-event-id": true,
	"idempotency-key": true, "x-idempotency-key": true,
}

type boundedReadCloser struct {
	body       io.ReadCloser
	remaining  int64
	overflowed bool
	onOverflow func()
}

type cancelBody struct {
	io.ReadCloser
	stop   func() bool
	cancel context.CancelFunc
	done   chan struct{}
	once   sync.Once
}

func newCancelBody(body io.ReadCloser, connCtx context.Context, stop func() bool, cancel context.CancelFunc) *cancelBody {
	b := &cancelBody{ReadCloser: body, stop: stop, cancel: cancel, done: make(chan struct{})}
	go func() {
		select {
		case <-connCtx.Done():
			_ = b.Close()
		case <-b.done:
		}
	}()
	return b
}

func (b *cancelBody) Close() error {
	var err error
	b.once.Do(func() {
		close(b.done)
		if b.stop != nil {
			b.stop()
		}
		if b.cancel != nil {
			b.cancel()
		}
		err = b.ReadCloser.Close()
	})
	return err
}

func (b *boundedReadCloser) Read(p []byte) (int, error) {
	if b.overflowed {
		return 0, errLimit("HTTP response body")
	}
	if b.remaining < 0 {
		b.overflowed = true
		if b.onOverflow != nil {
			b.onOverflow()
		}
		return 0, errLimit("HTTP response body")
	}
	if b.remaining == 0 {
		var probe [1]byte
		n, err := b.body.Read(probe[:])
		if n > 0 {
			b.overflowed = true
			if b.onOverflow != nil {
				b.onOverflow()
			}
			return 0, errLimit("HTTP response body")
		}
		return 0, err
	}
	if int64(len(p)) > b.remaining {
		p = p[:b.remaining]
	}
	n, err := b.body.Read(p)
	b.remaining -= int64(n)
	return n, err
}

func (b *boundedReadCloser) Close() error { return b.body.Close() }

func (c *connection) latchClass(class string) {
	if class == "" {
		return
	}
	c.errClass.CompareAndSwap("", class)
}

func (c *connection) latchedClass() string {
	class, _ := c.errClass.Load().(string)
	return class
}

func (c *connection) markListenerStarted() {
	c.listenerOnce.Do(func() { close(c.listenerStarted) })
}

func (c *connection) terminalClass() string {
	switch c.latchedClass() {
	case "auth", "certificate", "rate-limit", "server", "limit", "session-lost":
		return c.latchedClass()
	}
	return ""
}

func (c *connection) clearRetryAdvice() {
	c.retryMu.Lock()
	c.retryDelay = 0
	c.retryStop = false
	c.retryMu.Unlock()
}

func (c *connection) recordRetryAfter(values []string) {
	c.retryMu.Lock()
	defer c.retryMu.Unlock()
	if len(values) == 0 {
		return
	}
	if len(values) > 1 {
		c.retryStop = true
		return
	}
	value := strings.TrimSpace(values[0])
	if value == "" {
		c.retryStop = true
		return
	}
	if seconds, err := strconv.Atoi(value); err == nil {
		if seconds < 0 || seconds > 5 {
			c.retryStop = true
			return
		}
		c.retryDelay = time.Duration(seconds) * time.Second
		return
	}
	when, err := http.ParseTime(value)
	if err != nil {
		c.retryStop = true
		return
	}
	delay := when.Sub(c.manager.opts.now())
	if delay < 0 {
		delay = 0
	}
	if delay > 5*time.Second {
		c.retryStop = true
		return
	}
	c.retryDelay = delay
}

func (c *connection) retryAdvice() (time.Duration, bool) {
	c.retryMu.Lock()
	defer c.retryMu.Unlock()
	return c.retryDelay, !c.retryStop
}
