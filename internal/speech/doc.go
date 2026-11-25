// Package speech defines the speech engine interfaces (VAD, WakeWord, STT and
// TTS), their supporting types and their by-name registries. Engines live in
// subpackages (sherpa, cloud providers) and register themselves; fake holds
// scriptable engines for tests.
package speech
