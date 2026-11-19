package mcp

import (
	"net/url"
	"strconv"
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
