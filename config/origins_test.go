package config

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Nothing set: the tag defaults survive (a bad tag default makes configor
// drop them all, see AppConfig.AllowedOrigins) and Validate passes.
func TestLoadConfig_OriginsDefaults(t *testing.T) {
	cfg := LoadConfigOrPanic()
	assert.Equal(t, "http://localhost:5173", cfg.AppConfig.AllowedOrigins)
	assert.Empty(t, cfg.AppConfig.OriginsURL)
	assert.Zero(t, cfg.AppConfig.OriginsRefresh, "unset; the fetcher uses 5m")
	assert.Equal(t, 3000, cfg.AppConfig.Port, "other defaults still applied")
	assert.Equal(t, "boson", cfg.DBConfig.User)
	assert.False(t, cfg.AppConfig.UsesDirectory())
	require.NoError(t, cfg.Validate())
}

func TestLoadConfig_OriginsFromEnv(t *testing.T) {
	t.Setenv("ALLOWED_ORIGINS", "directory,https://chat.boson.chat")
	t.Setenv("ORIGINS_URL", "http://auth:8080/v1/web-origins")
	t.Setenv("ORIGINS_REFRESH", "2m")
	cfg := LoadConfigOrPanic()
	assert.Equal(t, "directory,https://chat.boson.chat", cfg.AppConfig.AllowedOrigins)
	assert.Equal(t, "http://auth:8080/v1/web-origins", cfg.AppConfig.OriginsURL)
	assert.Equal(t, 2*time.Minute, cfg.AppConfig.OriginsRefresh)
	assert.True(t, cfg.AppConfig.UsesDirectory())
	require.NoError(t, cfg.Validate())
	assert.Equal(t, 3000, cfg.AppConfig.Port)
}

func TestValidate_DirectoryNeedsOriginsURL(t *testing.T) {
	c := Config{AppConfig: AppConfig{AllowedOrigins: "https://chat.a.test, Directory"}}
	require.True(t, c.AppConfig.UsesDirectory())
	err := c.Validate()
	require.Error(t, err)
	assert.True(t, strings.Contains(err.Error(), "ORIGINS_URL"), err.Error())

	c.AppConfig.OriginsURL = "  "
	require.Error(t, c.Validate(), "blank ORIGINS_URL")

	c.AppConfig.OriginsURL = "http://auth:8080/v1/web-origins"
	require.NoError(t, c.Validate())

	require.NoError(t, Config{AppConfig: AppConfig{AllowedOrigins: "https://chat.a.test"}}.Validate())
}
