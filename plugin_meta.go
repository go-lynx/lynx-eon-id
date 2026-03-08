package eonId

// Plugin metadata
const (
	PluginName        = "eon-id"
	PluginVersion     = "v1.5.5"
	PluginDescription = "Eon-ID generator plugin with clock drift protection and Redis-based worker ID management"
	ConfPrefix        = "lynx.eon-id"
)

// NewSnowflakeGenerator creates an eon-id plugin instance; kept for backward compatibility.
func NewSnowflakeGenerator() *PlugSnowflake {
	return NewSnowflakePlugin()
}
