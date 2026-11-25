// Package llm defines the language model Provider interface, the request and
// streamed-response types, and the by-name provider registry. Providers
// (Claude, OpenAI, Gemini, Ollama) live in subpackages and register
// themselves; fake holds a scriptable provider for tests.
package llm
