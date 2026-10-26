// Package provider adapts normalized context to one API (Phase 6: Anthropic).
package provider

import "context"

// Request is the normalized model request.
type Request struct {
	Model    string
	Messages []any
	Tools    []any
}

// Response is one authoritative completed message.
type Response struct {
	Text string
}

// Provider streams text and returns one completed message.
type Provider interface {
	Send(ctx context.Context, req Request) (*Response, error)
}
