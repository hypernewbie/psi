package main

import (
	"fmt"
	"regexp"
	"strings"
)

// Configuration templates accept an underscore in the first position, unlike
// the stricter grammar for a whole-value environment secret reference.
var configEnvName = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)

// substituteTokenEnvironment borrows the token string. The configuration and
// Discord APIs require strings, so the builder and result may retain copies.
func substituteTokenEnvironment(value string, lookupEnv func(string) (string, bool)) (string, error) {
	var out strings.Builder
	end := 0
	for index := 0; index < len(value); index++ {
		if value[index] != '$' {
			continue
		}
		escaped := strings.HasPrefix(value[index:], "$${")
		start := index + 2
		if escaped {
			start++
		} else if !strings.HasPrefix(value[index:], "${") {
			continue
		}
		closeOffset := strings.IndexByte(value[start:], '}')
		if closeOffset < 0 {
			continue
		}
		closeIndex := start + closeOffset
		body := value[start:closeIndex]
		name, fallback, hasFallback := strings.Cut(body, ":-")
		if !configEnvName.MatchString(name) || (hasFallback && strings.ContainsAny(fallback, "${")) {
			continue
		}
		out.WriteString(value[end:index])
		if escaped {
			out.WriteString(value[index+1 : closeIndex+1])
		} else if resolved, ok := lookupEnv(name); ok && resolved != "" {
			out.WriteString(resolved)
		} else if hasFallback {
			out.WriteString(fallback)
		} else {
			return "", fmt.Errorf("environment secret is unavailable")
		}
		end = closeIndex + 1
		index = closeIndex
	}
	out.WriteString(value[end:])
	return out.String(), nil
}
