package app

import (
	"encoding/json"
	"fmt"
	"path/filepath"

	"psi/internal/config"
)

// persistSettingsValue merges one value into the agent settings file.
func (r *runtime) persistSettingsValue(path []string, value any) error {
	if path == nil {
		return nil
	}
	if r.agentDir == "" {
		return fmt.Errorf("agent directory is unavailable")
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return config.UpdateUserSettings(filepath.Join(r.agentDir, "settings.json"), []config.SettingsUpdate{
		{Path: path, Value: raw},
	})
}
