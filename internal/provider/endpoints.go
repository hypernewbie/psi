package provider

import (
	"net/url"
	"strconv"
	"strings"

	"github.com/hypernewbie/psi/internal/providerregistry"
)

// IsNonDefaultEndpoint reports whether baseURL is a valid base for
// providerID's api and its adapter request route differs from the registered
// default route. Invalid URLs are not eligible. It parses URLs only: it never
// resolves DNS or contacts the endpoint.
func IsNonDefaultEndpoint(providerID, api, baseURL string) bool {
	defaultBase := providerregistry.DefaultEndpoint(providerID, api)
	if defaultBase == "" {
		return false
	}
	effective, ok := adapterEndpoint(api, baseURL)
	if !ok {
		return false
	}
	official, ok := adapterEndpoint(api, defaultBase)
	if !ok {
		return false
	}
	effectivePort, ok := requestPort(effective)
	if !ok {
		return false
	}
	officialPort, ok := requestPort(official)
	if !ok {
		return false
	}
	sameHost := strings.EqualFold(strings.TrimSuffix(effective.Hostname(), "."), strings.TrimSuffix(official.Hostname(), "."))
	return effective.Scheme != official.Scheme || !sameHost || effectivePort != officialPort || effective.Path != official.Path
}

// adapterEndpoint parses the adapter's actual request route for the comparison.
// Query/fragment delimiters, even empty ones, put an appended route outside the path.
func adapterEndpoint(api, baseURL string) (*url.URL, bool) {
	var endpoint string
	switch api {
	case "openai-responses":
		var err error
		endpoint, err = responsesEndpoint(baseURL)
		if err != nil {
			return nil, false
		}
	case "anthropic-messages":
		endpoint = normalizeAnthropicBase(baseURL) + "/v1/messages"
	default:
		return nil, false
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Hostname() == "" || parsed.User != nil || strings.ContainsAny(endpoint, "?#") {
		return nil, false
	}
	return parsed, parsed.Scheme == "http" || parsed.Scheme == "https"
}

// IsOfficialOpenAIEndpoint reports whether baseURL is the exact official
// OpenAI Responses endpoint used by ChatGPT subscription requests. This is the
// existing OAuth destination check, deliberately stricter than the route
// comparison that permits optional API keys.
func IsOfficialOpenAIEndpoint(baseURL string) bool {
	return openAIOfficialBase(baseURL)
}

// normalizeAnthropicBase applies the existing client endpoint normalization.
func normalizeAnthropicBase(baseURL string) string {
	baseURL = strings.TrimSuffix(baseURL, "/")
	return strings.TrimSuffix(baseURL, "/v1")
}

// requestPort compares port numbers rather than spellings such as 0443.
func requestPort(parsed *url.URL) (uint64, bool) {
	if port := parsed.Port(); port != "" {
		number, err := strconv.ParseUint(port, 10, 16)
		return number, err == nil && number != 0
	}
	if parsed.Scheme == "https" {
		return 443, true
	}
	return 80, true
}
