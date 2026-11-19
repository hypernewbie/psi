package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	modelsDevURL  = "https://models.dev/experimental/v2.0/api.json"
	openRouterURL = "https://openrouter.ai/api/v1/models"

	// maxInputBytes bounds each decompressed catalog body. One extra byte is
	// read so an oversized body is detected instead of truncated.
	maxInputBytes = 32 << 20

	totalFetchBudget   = 120 * time.Second
	attemptFetchBudget = 30 * time.Second
	maxFetchAttempts   = 3
	retryAfterCap      = 10 * time.Second
)

// retryBackoff holds the delay before the second and third attempts. A valid
// Retry-After value can raise these delays but never lowers them.
var retryBackoff = []time.Duration{250 * time.Millisecond, time.Second}

var retryableStatus = map[int]bool{
	http.StatusRequestTimeout:      true,
	http.StatusTooManyRequests:     true,
	http.StatusInternalServerError: true,
	http.StatusBadGateway:          true,
	http.StatusServiceUnavailable:  true,
	http.StatusGatewayTimeout:      true,
}

type fetchedInput struct {
	body      []byte
	fetchedAt time.Time
}

type fetchedInputs struct {
	modelsDevBody       []byte
	modelsDevFetchedAt  time.Time
	openRouterBody      []byte
	openRouterFetchedAt time.Time
}

type fetchTarget struct {
	name     string
	url      string
	validate func([]byte) error
}

type fetchConfig struct {
	client *http.Client
	wait   func(context.Context, time.Duration) error
	now    func() time.Time
}

// newCatalogHTTPClient builds the direct client used by the live command. TLS
// verification stays on, redirects are refused, no cookie jar is used, and
// ambient proxy settings are ignored.
func newCatalogHTTPClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{ForceAttemptHTTP2: true},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func defaultFetchConfig() fetchConfig {
	return fetchConfig{client: newCatalogHTTPClient(), wait: waitContext, now: time.Now}
}

// fetchCatalogInputs performs the two sequential production GETs. The same
// client is reused for both sources.
func fetchCatalogInputs(ctx context.Context) (fetchedInputs, error) {
	results, err := fetchTargets(ctx, defaultFetchConfig(), []fetchTarget{
		{name: modelsDevInputName, url: modelsDevURL, validate: validateModelsDevJSON},
		{name: openRouterInputName, url: openRouterURL, validate: validateOpenRouterBody},
	})
	if err != nil {
		return fetchedInputs{}, err
	}
	return fetchedInputs{
		modelsDevBody:       results[modelsDevInputName].body,
		modelsDevFetchedAt:  results[modelsDevInputName].fetchedAt,
		openRouterBody:      results[openRouterInputName].body,
		openRouterFetchedAt: results[openRouterInputName].fetchedAt,
	}, nil
}

func validateModelsDevJSON(body []byte) error {
	var catalog modelsDevCatalog
	return json.Unmarshal(body, &catalog)
}

func validateOpenRouterBody(body []byte) error {
	var listing openRouterListing
	return json.Unmarshal(body, &listing)
}

func fetchTargets(ctx context.Context, cfg fetchConfig, targets []fetchTarget) (map[string]fetchedInput, error) {
	if cfg.client == nil {
		cfg.client = newCatalogHTTPClient()
	}
	if cfg.wait == nil {
		cfg.wait = waitContext
	}
	if cfg.now == nil {
		cfg.now = time.Now
	}
	ctx, cancel := context.WithTimeout(ctx, totalFetchBudget)
	defer cancel()
	out := make(map[string]fetchedInput, len(targets))
	for _, target := range targets {
		input, err := fetchOne(ctx, cfg, target)
		if err != nil {
			return nil, err
		}
		out[target.name] = input
	}
	return out, nil
}

func fetchOne(ctx context.Context, cfg fetchConfig, target fetchTarget) (fetchedInput, error) {
	for attempt := 1; attempt <= maxFetchAttempts; attempt++ {
		input, retryAfter, retryable, err := attemptFetch(ctx, cfg, target)
		if err == nil {
			return input, nil
		}
		if !retryable || attempt == maxFetchAttempts {
			return fetchedInput{}, err
		}
		delay := retryBackoff[attempt-1]
		if retryAfter > delay {
			delay = retryAfter
		}
		if delay > retryAfterCap {
			return fetchedInput{}, fmt.Errorf("%w (Retry-After exceeds the %s cap)", err, retryAfterCap)
		}
		if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) <= delay {
			return fetchedInput{}, fmt.Errorf("%w (no time left for another attempt)", err)
		}
		if waitErr := cfg.wait(ctx, delay); waitErr != nil {
			return fetchedInput{}, fmt.Errorf("%s: %w", target.name, waitErr)
		}
		if ctx.Err() != nil {
			return fetchedInput{}, fmt.Errorf("%s: %w", target.name, ctx.Err())
		}
	}
	return fetchedInput{}, fmt.Errorf("%s: retries exhausted", target.name)
}

// attemptFetch performs one bounded attempt. It closes the body before any
// retry decision and never includes response content in an error.
func attemptFetch(ctx context.Context, cfg fetchConfig, target fetchTarget) (fetchedInput, time.Duration, bool, error) {
	if err := ctx.Err(); err != nil {
		return fetchedInput{}, 0, false, fmt.Errorf("%s: %w", target.name, err)
	}
	attemptCtx, cancel := context.WithTimeout(ctx, attemptFetchBudget)
	defer cancel()
	request, err := http.NewRequestWithContext(attemptCtx, http.MethodGet, target.url, nil)
	if err != nil {
		return fetchedInput{}, 0, false, fmt.Errorf("%s: %w", target.name, err)
	}
	response, err := cfg.client.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return fetchedInput{}, 0, false, fmt.Errorf("%s: %w", target.name, ctx.Err())
		}
		var certificateError *tls.CertificateVerificationError
		if errors.As(err, &certificateError) {
			return fetchedInput{}, 0, false, fmt.Errorf("%s: TLS certificate verification failed", target.name)
		}
		return fetchedInput{}, 0, transientRequestError(err), fmt.Errorf("%s: request failed: %w", target.name, err)
	}
	// Status and Retry-After are authoritative even if the error body hangs
	// or is truncated. Do not spend the attempt budget reading it.
	if response.StatusCode != http.StatusOK {
		_ = response.Body.Close()
		err := fmt.Errorf("%s: HTTP %d", target.name, response.StatusCode)
		if retryableStatus[response.StatusCode] {
			return fetchedInput{}, parseRetryAfter(response.Header.Get("Retry-After"), cfg.now()), true, err
		}
		return fetchedInput{}, 0, false, err
	}
	body, readErr := io.ReadAll(io.LimitReader(response.Body, maxInputBytes+1))
	_ = response.Body.Close()
	if err := ctx.Err(); err != nil {
		return fetchedInput{}, 0, false, fmt.Errorf("%s: %w", target.name, err)
	}
	if readErr == nil {
		readErr = attemptCtx.Err()
	}
	if readErr != nil {
		return fetchedInput{}, 0, true, fmt.Errorf("%s: interrupted body read: %w", target.name, readErr)
	}
	fetchedAt := cfg.now().UTC()
	if len(body) > maxInputBytes {
		return fetchedInput{}, 0, false, fmt.Errorf("%s: response exceeds %d bytes", target.name, maxInputBytes)
	}
	if err := target.validate(body); err != nil {
		return fetchedInput{}, 0, false, fmt.Errorf("%s: invalid JSON: %v", target.name, err)
	}
	if err := ctx.Err(); err != nil {
		return fetchedInput{}, 0, false, fmt.Errorf("%s: %w", target.name, err)
	}
	return fetchedInput{body: body, fetchedAt: fetchedAt}, 0, false, nil
}

func transientRequestError(err error) bool {
	// Client.Do wraps both network errors and permanent client errors (for
	// example, an invalid redirect Location) in a URL error.
	var requestErr *url.Error
	if errors.As(err, &requestErr) {
		err = requestErr.Err
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) && dnsErr.IsNotFound {
		return false
	}
	var opErr *net.OpError
	var netErr net.Error
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, context.DeadlineExceeded) || errors.As(err, &opErr) ||
		errors.As(err, &netErr) && netErr.Timeout()
}

// parseRetryAfter accepts delta seconds or an HTTP date. A malformed or past
// value is treated as absent, as the pinned rule requires.
func parseRetryAfter(value string, now time.Time) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	if strings.Trim(value, "0123456789") == "" {
		seconds, err := strconv.ParseUint(value, 10, 64)
		if err != nil || seconds > uint64(math.MaxInt64/int64(time.Second)) {
			// A valid but unrepresentable delay must still prevent an early retry.
			return time.Duration(math.MaxInt64)
		}
		return time.Duration(seconds) * time.Second
	}
	if when, err := http.ParseTime(value); err == nil {
		if delay := when.Sub(now); delay > 0 {
			return delay
		}
	}
	return 0
}

func waitContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
