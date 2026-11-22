package main

import (
	"fmt"
	"strings"
)

func (s *fileSettings) validateSecretTokenReference(lookupEnv func(string) (string, bool)) error {
	ref := s.Channels.Discord.Token
	switch {
	case ref.env != "":
		if err := s.validateEnvTokenProvider(); err != nil {
			return err
		}
		_, err := ref.resolve(lookupEnv)
		return err
	case ref.file != "":
		_, err := s.fileTokenProvider()
		return err
	case ref.exec != "":
		alias, provider, err := s.secretProviderForRef("exec", ref.provider)
		if err != nil || provider == nil {
			return fmt.Errorf("exec secret provider is unavailable")
		}
		return validateSecretExecConfig(alias, provider, ref.exec, lookupEnv)
	case ref.store != "":
		_, _, err := s.secretProviderForRef("store", ref.provider)
		if err != nil {
			return err
		}
		if !secretEnvName.MatchString(ref.store) {
			return fmt.Errorf("invalid store secret reference")
		}
		return nil
	default:
		_, err := ref.resolve(lookupEnv)
		return err
	}
}

func (s *fileSettings) secretProviderForRef(source, explicit string) (string, *secretProvider, error) {
	alias := strings.TrimSpace(explicit)
	if alias == "" && s.Secrets != nil {
		switch source {
		case "env":
			alias = strings.TrimSpace(s.Secrets.Defaults.Env)
		case "file":
			alias = strings.TrimSpace(s.Secrets.Defaults.File)
		case "exec":
			alias = strings.TrimSpace(s.Secrets.Defaults.Exec)
		case "store":
			alias = strings.TrimSpace(s.Secrets.Defaults.Store)
		}
	}
	if alias == "" {
		alias = "default"
	}
	if !secretProviderName.MatchString(alias) {
		return "", nil, fmt.Errorf("invalid secret provider alias")
	}
	var configured *secretProvider
	defaultAlias := "default"
	if s.Secrets != nil {
		configured = s.Secrets.Providers[alias]
		switch source {
		case "env":
			if value := strings.TrimSpace(s.Secrets.Defaults.Env); value != "" {
				defaultAlias = value
			}
		case "store":
			if value := strings.TrimSpace(s.Secrets.Defaults.Store); value != "" {
				defaultAlias = value
			}
		}
	}
	if configured == nil || configured.Source != source {
		if alias == defaultAlias && (source == "env" || source == "store") {
			return alias, nil, nil
		}
		return "", nil, fmt.Errorf("secret provider is unavailable")
	}
	return alias, configured, nil
}
