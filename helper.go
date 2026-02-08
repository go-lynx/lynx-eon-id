package eonId

import (
	"fmt"

	"github.com/go-lynx/lynx"
)

// GetEonIdPlugin returns the eon-id plugin instance from the Lynx app (only available after the plugin is initialized and started).
func GetEonIdPlugin() (*PlugSnowflake, error) {
	if lynx.Lynx() == nil {
		return nil, fmt.Errorf("lynx application not initialized")
	}
	pm := lynx.Lynx().GetPluginManager()
	if pm == nil {
		return nil, fmt.Errorf("plugin manager not available")
	}
	plugin := pm.GetPlugin(PluginName)
	if plugin == nil {
		return nil, fmt.Errorf("plugin %s not found", PluginName)
	}
	snowflakePlugin, ok := plugin.(*PlugSnowflake)
	if !ok {
		return nil, fmt.Errorf("plugin %s is not eon-id plugin", PluginName)
	}
	return snowflakePlugin, nil
}

// GenerateID generates a new unique ID using the global eon-id plugin.
func GenerateID() (int64, error) {
	plugin, err := GetEonIdPlugin()
	if err != nil {
		return 0, err
	}

	return plugin.GenerateID()
}

// GenerateIDWithMetadata generates an ID with metadata using the global eon-id plugin.
func GenerateIDWithMetadata() (int64, *SID, error) {
	plugin, err := GetEonIdPlugin()
	if err != nil {
		return 0, nil, err
	}

	return plugin.GenerateIDWithMetadata()
}

// ParseID parses an ID and returns its metadata using the global eon-id plugin.
func ParseID(id int64) (*SID, error) {
	plugin, err := GetEonIdPlugin()
	if err != nil {
		return nil, err
	}

	return plugin.ParseID(id)
}

// GetGenerator returns the underlying Generator instance from the global plugin.
func GetGenerator() (*Generator, error) {
	plugin, err := GetEonIdPlugin()
	if err != nil {
		return nil, err
	}

	generator := plugin.GetGenerator()
	if generator == nil {
		return nil, fmt.Errorf("eon-id generator is not initialized")
	}

	return generator, nil
}

// CheckHealth checks the health of the eon-id plugin.
func CheckHealth() error {
	plugin, err := GetEonIdPlugin()
	if err != nil {
		return err
	}

	return plugin.CheckHealth()
}
