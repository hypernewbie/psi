package main

import (
	"fmt"
	"strings"
)

func (s *fileSettings) resolveDiscordToken(lookupEnv func(string) (string, bool), storeDatabase string) (string, error) {
	ref := s.Channels.Discord.Token
	switch {
	case ref.exec != "":
		return s.execToken(ref, lookupEnv)
	case ref.store != "":
		return s.storeToken(ref, storeDatabase)
	case ref.file != "":
		return s.resolveConfiguredToken(lookupEnv)
	case ref.env != "":
		value, ok := lookupEnv(ref.env)
		if !ok || strings.TrimSpace(value) == "" {
			return "", fmt.Errorf("environment secret is unavailable")
		}
		return value, nil
	default:
		return s.resolveConfiguredToken(lookupEnv)
	}
}

func resolveFallbackDiscordToken(lookupEnv func(string) (string, bool)) (string, error) {
	value, ok := lookupEnv("DISCORD_BOT_TOKEN")
	value = strings.TrimSpace(value)
	if !ok || value == "" {
		return "", fmt.Errorf("settings: no Discord bot token and DISCORD_BOT_TOKEN is not set")
	}
	return value, nil
}
