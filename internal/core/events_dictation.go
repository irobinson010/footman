package core

import "github.com/irobinson010/footman/internal/bus"

// Dictation session phases, in DictationStatus.Phase (ADR-0045).
const (
	// PhaseChoosing waits for the user to pick one of several files by
	// number.
	PhaseChoosing = "choosing"
	// PhaseDictating types every utterance, with no wake word.
	PhaseDictating = "dictating"
)

// Reasons a dictation session ends, in DictationStatus.Reason.
const (
	EndStopPhrase = "stop_phrase" // the user said a stop phrase
	EndIdle       = "idle"        // nothing was said for dictation.idle_timeout
	EndStopped    = "stopped"     // the break-glass stop
	EndMicrophone = "microphone"  // the microphone stream ended
	EndListening  = "listening_off"
	EndCancelled  = "cancelled" // the user cancelled the file choice
	EndError      = "error"     // the file could not be opened
	EndShutdown   = "shutdown"  // the daemon is stopping
)

// DictationStatus is published when a voice-started dictation session starts,
// changes phase or ends. It is the persistent status: Active stays true while
// the session lasts, and the tray shows the microphone as recording.
type DictationStatus struct {
	Active bool
	// Phase is PhaseChoosing or PhaseDictating while Active.
	Phase string `json:",omitempty"`
	// Message is the text shown, such as "Dictating — say 'stop dictation'".
	Message string
	// Target is the file being dictated into, or empty for the focused app.
	Target string `json:",omitempty"`
	// Reason says why an ended session ended (one of the End* values).
	Reason string `json:",omitempty"`
}

// DictationChanged reports voice-started dictation sessions (ADR-0045).
var DictationChanged = bus.NewTopic[DictationStatus]("dictation.status")
