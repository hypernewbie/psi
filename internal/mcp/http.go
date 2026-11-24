package mcp

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"mime"
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
		// Bound the lifetime of a socket returned to the pool after cancellation.
		IdleConnTimeout:   90 * time.Second,
		ForceAttemptHTTP2: true,
	}
	conn.closeIdleConnections = base.CloseIdleConnections
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
	if err != nil || parsed.Scheme == "" || parsed.Hostname() == "" {
		return nil, configErr("config", "MCP url must be an absolute http or https URL")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, configErr("config", "MCP url must use http or https")
	}
	if port := parsed.Port(); port != "" {
		n, err := strconv.ParseUint(port, 10, 16)
		if err != nil || n == 0 {
			return nil, configErr("config", "MCP url port must be between 1 and 65535")
		}
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

// requestObservation carries HTTP response metadata for one MCP invocation.
// It is deliberately one-use: detached SDK requests can inherit a parent
// context, but only the first marked POST may claim the observation.
type requestObservation struct {
	method string

	mu       sync.Mutex
	claimed  bool
	finished bool
	class    string
	status   int
}

type requestObservationKey struct{}
type outboundObservationKey struct{}

func withRequestObservation(ctx context.Context, obs *requestObservation) context.Context {
	return context.WithValue(ctx, requestObservationKey{}, obs)
}

func requestObservationFrom(ctx context.Context) *requestObservation {
	obs, _ := ctx.Value(requestObservationKey{}).(*requestObservation)
	return obs
}

// withOutboundObservation marks the context passed to the method handler. A
// detached notification can inherit requestObservation, but not this marker
// unless it was derived inside the marked call.
func withOutboundObservation(ctx context.Context, obs *requestObservation) context.Context {
	return context.WithValue(ctx, outboundObservationKey{}, obs)
}

func outboundObservationFrom(ctx context.Context) *requestObservation {
	obs, _ := ctx.Value(outboundObservationKey{}).(*requestObservation)
	return obs
}

func newRequestObservation(method string) *requestObservation {
	return &requestObservation{method: method}
}

func (o *requestObservation) claim(status int, class string) {
	if o == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.finished || o.claimed {
		return
	}
	o.claimed = true
	o.status = status
	if class != "" {
		o.class = class
	}
}

func (o *requestObservation) latch(class string) {
	if o == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.finished {
		return
	}
	if class == "limit" || o.class == "" {
		o.class = class
	}
}

func (o *requestObservation) finish() {
	if o == nil {
		return
	}
	o.mu.Lock()
	o.finished = true
	o.mu.Unlock()
}

func (o *requestObservation) snapshot() (string, int) {
	if o == nil {
		return "", 0
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.class, o.status
}

type headerRoundTripper struct {
	base    http.RoundTripper
	headers map[string]string
	conn    *connection
}

func (t *headerRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL == nil {
		return nil, errors.New("MCP request has no URL")
	}
	if t.conn == nil {
		return nil, errors.New("MCP transport has no connection")
	}
	clone := req.Clone(req.Context())
	clone.Header = req.Header.Clone()
	baseObs := requestObservationFrom(req.Context())
	claimObs := outboundObservationFrom(req.Context())
	for name, value := range t.headers {
		if _, reserved := reservedHeaders[strings.ToLower(name)]; reserved {
			return nil, fmt.Errorf("MCP header %q is reserved", name)
		}
		if !validHeaderValue(value) {
			return nil, fmt.Errorf("MCP header %q contains an invalid value", name)
		}
		clone.Header.Set(name, value)
	}
	// The SDK sends a single stateful DELETE during Close with its own
	// deadline. Retiring the connection cancels normal I/O, not that request.
	if req.Method == http.MethodDelete && clone.Header.Get("Mcp-Session-Id") != "" {
		if _, bounded := clone.Context().Deadline(); bounded {
			return t.base.RoundTrip(clone)
		}
	}
	ctx, cancel := context.WithCancel(clone.Context())
	stopConn := context.AfterFunc(t.conn.ctx, cancel)
	// AfterFunc runs asynchronously. A request starting after retirement
	// must not reopen the closed connection pool.
	if t.conn.ctx.Err() != nil {
		cancel()
	}
	clone = clone.WithContext(ctx)
	if req.Method == http.MethodPost {
		clone.GetBody = nil
	}
	if req.Method == http.MethodGet {
		// Discovery waits for the GET outcome, not just the HTTP headers.
		defer t.conn.markListenerChecked()
	}
	resp, err := t.base.RoundTrip(clone)
	if err != nil {
		stopConn()
		cancel()
		t.recordTransportError(baseObs, claimObs, req, err)
		return nil, err
	}
	if resp.Body != nil {
		resp.Body = newCancelBody(resp.Body, t.conn.ctx, clone.Context(), stopConn, cancel)
	} else {
		stopConn()
		cancel()
	}
	mediaType, _, mediaTypeErr := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	isSSE := mediaTypeErr == nil && mediaType == "text/event-stream"
	if req.Method == http.MethodPost && baseObs != nil {
		if claimObs != nil {
			status := classifyHTTPStatus(resp.StatusCode, req.Method, req.Header.Get("Mcp-Session-Id"), isSSE)
			claimObs.claim(resp.StatusCode, observedClass(resp.StatusCode, status.class))
		}
	} else {
		t.recordConnectionResponse(req, resp, isSSE)
	}
	// The SDK can read an error body as a whole before checking its media
	// type. Only successful responses delivered as SSE use the event limit.
	streaming := isSSE && (req.Method == http.MethodGet && resp.StatusCode == http.StatusOK ||
		req.Method == http.MethodPost && resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices)
	if !streaming {
		resp.Body = &boundedReadCloser{body: resp.Body, remaining: maxHTTPBody, onOverflow: func() {
			// On a successful JSON response the SDK treats a body read error as
			// session failure and loses the typed cause. Preserve it before Read returns.
			if baseObs == nil || resp.StatusCode >= http.StatusOK && resp.StatusCode < http.StatusMultipleChoices {
				t.conn.latchClass("limit")
			}
			if req.Method == http.MethodPost && claimObs != nil {
				claimObs.latch("limit")
			}
		}}
	}
	return resp, nil
}

type httpStatusClass struct {
	class               string
	transient           bool
	listenerUnavailable bool
}

func classifyHTTPStatus(status int, method, sessionID string, sse bool) httpStatusClass {
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return httpStatusClass{class: "auth"}
	case status == http.StatusNotFound && sessionID != "" && (method == http.MethodPost || method == http.MethodGet):
		return httpStatusClass{class: "session-lost"}
	case method == http.MethodGet && status == http.StatusMethodNotAllowed:
		return httpStatusClass{listenerUnavailable: true}
	case method == http.MethodGet && status == http.StatusOK && !sse:
		return httpStatusClass{listenerUnavailable: true}
	case method == http.MethodGet && status == http.StatusOK:
		return httpStatusClass{}
	case method == http.MethodGet:
		if class := observedClass(status, ""); class != "" {
			return httpStatusClass{class: class, transient: true}
		}
		return httpStatusClass{class: "listener"}
	case status == http.StatusRequestTimeout || status == http.StatusTooManyRequests || status >= 500:
		return httpStatusClass{transient: true}
	default:
		return httpStatusClass{}
	}
}

func observedClass(status int, class string) string {
	if class != "" {
		return class
	}
	switch {
	case status == http.StatusRequestTimeout:
		return "timeout"
	case status == http.StatusTooManyRequests:
		return "rate-limit"
	case status >= 500:
		return "server"
	default:
		return ""
	}
}

func (t *headerRoundTripper) recordConnectionResponse(req *http.Request, resp *http.Response, isSSE bool) {
	status := classifyHTTPStatus(resp.StatusCode, req.Method, req.Header.Get("Mcp-Session-Id"), isSSE)
	if status.transient {
		t.conn.recordRetryAfter(resp.Header.Values("Retry-After"))
		t.conn.transient.Store(true)
	}
	if status.class != "" {
		t.conn.latchClass(status.class)
	}
	if status.listenerUnavailable {
		t.conn.listenerUnavailable.Store(true)
	}
}

func (t *headerRoundTripper) recordTransportError(baseObs, claimObs *requestObservation, req *http.Request, err error) {
	class := classifyTransportError(err)
	if baseObs != nil && req.Method == http.MethodPost {
		if claimObs != nil {
			claimObs.latch(class)
		}
		return
	}
	if class != "" {
		t.conn.latchClass(class)
	}
	if req.Method == http.MethodGet && class == "" {
		t.conn.listenerUnavailable.Store(true)
	}
}

func classifyTransportError(err error) string {
	var unknown x509.UnknownAuthorityError
	var hostname x509.HostnameError
	var invalid x509.CertificateInvalidError
	if errors.As(err, &unknown) || errors.As(err, &hostname) || errors.As(err, &invalid) {
		return "certificate"
	}
	return ""
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

func newCancelBody(body io.ReadCloser, connCtx, requestCtx context.Context, stop func() bool, cancel context.CancelFunc) *cancelBody {
	b := &cancelBody{ReadCloser: body, stop: stop, cancel: cancel, done: make(chan struct{})}
	go func() {
		select {
		case <-connCtx.Done():
			_ = b.Close()
		case <-requestCtx.Done():
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
	if class == "limit" {
		// A known size violation takes precedence over the HTTP status.
		c.errClass.Store(class)
		return
	}
	c.errClass.CompareAndSwap("", class)
}

func (c *connection) latchedClass() string {
	class, _ := c.errClass.Load().(string)
	return class
}

func (c *connection) markListenerChecked() {
	c.listenerOnce.Do(func() { close(c.listenerChecked) })
}

func (c *connection) terminalClass() string {
	switch c.latchedClass() {
	case "auth", "certificate", "timeout", "rate-limit", "server", "limit", "session-lost", "listener":
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
