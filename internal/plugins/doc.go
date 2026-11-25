// Package plugins defines the Tool interface that every action Footman can take
// sits behind, its ToolSpec and Permission levels, and the registry of
// built-in tools. The plugin host (footman and MCP adapters) will adapt
// external plugins to the same interface. fake holds a scriptable tool for
// tests.
package plugins
