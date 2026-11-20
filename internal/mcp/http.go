package mcp

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

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

// StatusError carries safe HTTP status metadata.
type StatusError struct {
	Status    int
	Retryable bool
	Auth      bool
	Session   bool
}

func (e *StatusError) Error() string { return fmt.Sprintf("HTTP status %d", e.Status) }

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
	case "auth", "certificate", "rate-limit", "server", "limit", "session-lost", "listener":
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
