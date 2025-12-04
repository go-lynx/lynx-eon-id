package eonId

// Plugin metadata
const (
	PluginName        = "eon-id"
	PluginVersion     = "1.0.0"
	PluginDescription = "Eon-ID generator plugin with clock drift protection and Redis-based worker ID management"
	ConfPrefix        = "lynx.eon-id"
)

// NewSnowflakeGenerator creates a new snowflake generator plugin instance
// This is kept for backward compatibility
func NewSnowflakeGenerator() *PlugSnowflake {
	return NewSnowflakePlugin()
}
