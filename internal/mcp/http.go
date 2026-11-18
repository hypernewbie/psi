package mcp

import "net/url"

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
