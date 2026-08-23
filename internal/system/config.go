package system

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/scottzx/minis-context/internal/protocol"
)

// Config holds non-secret instance settings (§8.3). Credentials never live
// here, in the databases or in the Library.
type Config struct {
	InstanceID string `json:"instance_id"`
	Timezone   string `json:"timezone"`
	Language   string `json:"language"`
	CreatedAt  string `json:"created_at"`
	CreatedBy  string `json:"created_by_version"`

	// Capacity defaults are used only when a day has no explicit
	// daily_capacity row; the UI must label such days as "default".
	DefaultWeekdayMinutes int `json:"default_weekday_minutes"`
	DefaultWeekendMinutes int `json:"default_weekend_minutes"`

	// JournalMode is decided per instance by `doctor storage` (§13.2) rather
	// than fixed in the design.
	JournalMode string `json:"journal_mode"`
	BusyTimeout int    `json:"busy_timeout_ms"`

	Privacy PrivacyConfig `json:"privacy"`
}

// PrivacyConfig sets the defaults inherited by newly captured material
// (§12.2 of the B+ design).
type PrivacyConfig struct {
	DefaultSensitivity string `json:"default_sensitivity"`
	DefaultCloudPolicy string `json:"default_cloud_policy"`
}

func DefaultConfig(instanceID, cliVersion string) Config {
	return Config{
		InstanceID:            instanceID,
		Timezone:              "Asia/Shanghai",
		Language:              "zh-CN",
		CreatedAt:             time.Now().UTC().Format(time.RFC3339),
		CreatedBy:             cliVersion,
		DefaultWeekdayMinutes: 240,
		DefaultWeekendMinutes: 120,
		JournalMode:           "wal",
		BusyTimeout:           5000,
		Privacy: PrivacyConfig{
			DefaultSensitivity: "normal",
			DefaultCloudPolicy: "summary_only",
		},
	}
}

func LoadConfig(l Layout) (Config, error) {
	raw, err := os.ReadFile(l.ConfigPath())
	if err != nil {
		if os.IsNotExist(err) {
			return Config{}, protocol.NotFound("no Minis instance at %s (run `minis init`)", l.Root)
		}
		return Config{}, protocol.Wrap(err, protocol.CodeIntegrity, "cannot read config")
	}
	var cfg Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return Config{}, protocol.Wrap(err, protocol.CodeIntegrity, "config.json is not valid JSON")
	}
	return cfg, nil
}

// SaveConfig writes the config atomically so a crash cannot leave a
// half-written instance descriptor behind.
func SaveConfig(l Layout, cfg Config) error {
	raw, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return protocol.Wrap(err, protocol.CodeInternal, "cannot encode config")
	}
	return WriteFileAtomic(l.ConfigPath(), append(raw, '\n'), 0o600)
}

// WriteFileAtomic writes via a temp file in the same directory, fsyncs it and
// renames into place.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return protocol.Wrap(err, protocol.CodeIntegrity, "cannot create directory")
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return protocol.Wrap(err, protocol.CodeIntegrity, "cannot create temp file")
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return protocol.Wrap(err, protocol.CodeIntegrity, "cannot write temp file")
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return protocol.Wrap(err, protocol.CodeIntegrity, "cannot fsync temp file")
	}
	if err := tmp.Close(); err != nil {
		return protocol.Wrap(err, protocol.CodeIntegrity, "cannot close temp file")
	}
	if err := os.Chmod(tmpName, perm); err != nil {
		return protocol.Wrap(err, protocol.CodeIntegrity, "cannot set permissions")
	}
	if err := os.Rename(tmpName, path); err != nil {
		return protocol.Wrap(err, protocol.CodeIntegrity, "cannot rename into place")
	}
	return nil
}
