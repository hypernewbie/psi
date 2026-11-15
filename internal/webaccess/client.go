package webaccess

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"psi/internal/tools"
)

// Client owns immutable configuration, guarded transports, and the store. The
// runtime creates one and keeps it across model, session, and reload changes.
type Client struct {
	cfg      Config
	page     *http.Client
	provider *http.Client
	store    *store
	mcpURL   string
	now      func() time.Time
	redactor redactor
}

// New builds a client from a validated configuration. Callers must not reuse
// the configuration for another client in the same runtime.
func New(cfg Config) *Client {
	strict := &guardedDialer{
		allow:   cfg.AllowRanges,
		resolve: defaultResolver,
		dial:    defaultDialer().DialContext,
	}
	page := &http.Client{
		Transport:     newHTTPTransport(strict.DialContext),
		CheckRedirect: pageRedirect,
	}

	providerDial := &guardedDialer{
		allow:   cfg.AllowRanges,
		resolve: defaultResolver,
		dial:    defaultDialer().DialContext,
	}
	if origin := loopbackOrigin(cfg.SearXNGBaseURL); origin != "" {
		providerDial.loopbackOrigin = origin
	}
	provider := &http.Client{
		Transport:     newHTTPTransport(providerDial.DialContext),
		CheckRedirect: providerRedirect,
	}
	return &Client{cfg: cfg, page: page, provider: provider, store: newStore(cfg.CacheMaxEntries, cfg.CacheMaxBytes), mcpURL: "https://mcp.exa.ai/mcp?tools=web_search_exa", redactor: newRedactor(cfg)}
}

func loopbackOrigin(base string) string {
	if base == "" {
		return ""
	}
	u, err := url.Parse(base)
	if err != nil || !isLoopbackHost(u.Hostname()) {
		return ""
	}
	port := u.Port()
	if port == "" {
		if strings.EqualFold(u.Scheme, "https") {
			port = "443"
		} else {
			port = "80"
		}
	}
	return netJoin(u.Hostname(), port)
}

func netJoin(host, port string) string {
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		return "[" + host + "]:" + port
	}
	return host + ":" + port
}

// CloseIdle releases connections owned by both pools.
func (c *Client) CloseIdle() {
	if c == nil {
		return
	}
	c.page.CloseIdleConnections()
	c.provider.CloseIdleConnections()
}

func pageRedirect(req *http.Request, via []*http.Request) error {
	if len(via) > 5 {
		return fmt.Errorf("%s: too many redirects", classBlocked)
	}
	previous := via[len(via)-1]
	if strings.EqualFold(previous.URL.Scheme, "https") && strings.EqualFold(req.URL.Scheme, "http") {
		return fmt.Errorf("%s: https to http redirect is not allowed", classBlocked)
	}
	if _, err := validateTarget(req.URL.String()); err != nil {
		return err
	}
	return nil
}

func providerRedirect(req *http.Request, via []*http.Request) error {
	if len(via) > 5 {
		return fmt.Errorf("%s: too many provider redirects", classBlocked)
	}
	origin := via[0].URL
	if !strings.EqualFold(origin.Scheme, req.URL.Scheme) || !strings.EqualFold(origin.Host, req.URL.Host) {
		return fmt.Errorf("%s: cross-origin provider redirect is not allowed", classBlocked)
	}
	if _, err := validateTarget(req.URL.String()); err != nil {
		return fmt.Errorf("%s: provider redirect target is invalid", classValidation)
	}
	return nil
}

// Dispatch runs one web tool invocation. Handlers recheck enablement so a
// fabricated internal call cannot bypass the advertised set.
func (c *Client) Dispatch(ctx context.Context, inv tools.Invocation) tools.Result {
	if err := ctx.Err(); err != nil {
		err = wrapClass(ctx, classCancelled, "tool cancelled")
		switch inv.Name {
		case ToolSearch:
			return searchError(err)
		case ToolContent:
			return contentError(err)
		case ToolFetch:
			return fetchError(err)
		default:
			text, _ := boundResult(fmt.Sprintf("web access failed: %v", err))
			return tools.Result{IsError: true, Content: []tools.Block{{Type: "text", Text: text}}}
		}
	}
	switch inv.Name {
	case ToolSearch:
		return c.handleSearch(ctx, inv)
	case ToolFetch:
		return c.handleFetch(ctx, inv)
	case ToolContent:
		return c.handleContent(ctx, inv)
	default:
		text, _ := boundResult(fmt.Sprintf("unknown web tool %q", inv.Name))
		return tools.Result{IsError: true, Content: []tools.Block{{Type: "text", Text: text}}}
	}
}
