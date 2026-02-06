package eonId

import (
	"github.com/go-lynx/lynx/pkg/factory"
	"github.com/go-lynx/lynx/plugins"
)

// init registers the eon-id plugin with the global factory (name and ConfPrefix must match).
func init() {
	factory.GlobalTypedFactory().RegisterPlugin(PluginName, ConfPrefix, func() plugins.Plugin {
		return NewSnowflakePlugin()
	})
}

// GetSnowflakeGenerator returns the eon-id plugin instance (same as GetSnowflakePlugin). Prefer GetSnowflakePlugin or package-level GenerateID().
func GetSnowflakeGenerator() (*PlugSnowflake, error) {
	return GetSnowflakePlugin()
}
