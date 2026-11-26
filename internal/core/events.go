package core

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/irobinson010/footman/internal/bus"
	"github.com/irobinson010/footman/internal/dictation"
	"github.com/irobinson010/footman/internal/jobs"
	"github.com/irobinson010/footman/internal/platform"
)

// State is where the pipeline is (design §5).
type State int

// Pipeline states.
const (
	// Idle waits for the wake word or a hotkey.
	Idle State = iota + 1
	// Listening captures speech after the wake word or a hotkey.
	Listening
	// Transcribing turns the captured speech into text.
	Transcribing
	// Routing decides between dictation and a command (and checks for the
	// break-glass stop just before).
	Routing
	// Cleaning tidies dictated text: fillers, repeats, mid-dictation
	// commands.
	Cleaning
	// Inserting puts dictated text at the cursor.
	Inserting
	// Acknowledging starts the persona's acknowledgement clip.
	Acknowledging
	// FastPath runs a command matched locally, without the AI.
	FastPath
	// Thinking waits for the AI.
	Thinking
	// RunningTool runs a tool the AI asked for.
	RunningTool
	// Replying speaks or shows the reply.
	Replying
)

var stateNames = [...]string{
	Idle: "idle", Listening: "listening", Transcribing: "transcribing", Routing: "routing",
	Cleaning: "cleaning", Inserting: "inserting", Acknowledging: "acknowledging",
	FastPath: "fast_path", Thinking: "thinking", RunningTool: "running_tool", Replying: "replying",
}

// String returns the state's snake_case name, as used in API events.
func (s State) String() string {
	if s >= Idle && s <= Replying {
		return stateNames[s]
	}
	return fmt.Sprintf("State(%d)", int(s))
}

// MarshalText implements encoding.TextMarshaler, so API events carry names.
func (s State) MarshalText() ([]byte, error) { return []byte(s.String()), nil }

// StateChange is published on every state change.
type StateChange struct {
	From, To State
	// Turn identifies the utterance being handled, or 0 when none is.
	Turn uint64
}

// Transcript is published when an utterance has been transcribed.
type Transcript struct {
	Turn    uint64
	Trigger Trigger
	Text    string
	// Audio is how long the transcribed audio was, and Took how long
	// speech-to-text took; both 0 for text that was not spoken.
	Audio, Took time.Duration
}

// Routed is published with the router's decision.
type Routed struct {
	Turn  uint64
	Route Route
}

// Insertion is published when dictated text has been inserted.
type Insertion struct {
	Turn   uint64
	Text   string
	Method dictation.InsertMethod
	// Latency is the time from the end of speech (the push-to-talk key
	// coming up) to the insertion finishing, or 0 when unknown.
	Latency time.Duration
	// Took is how long typing or pasting took.
	Took time.Duration
}

// Acknowledgement is published when the acknowledgement clip's first sample
// is handed to the audio player.
type Acknowledgement struct {
	Turn uint64
	// Latency is the time from the end of speech (the VAD handing over the
	// segment, or the push-to-talk key coming up) to the first sample, or 0
	// when unknown (text submitted through the API).
	Latency time.Duration
	// Lead is the time from the command starting to the first sample: how
	// long picking and starting the clip took.
	Lead time.Duration
	// Clip is the clip's length.
	Clip time.Duration
}

// FastPathRan is published when a command was handled by a fast-path rule.
type FastPathRan struct {
	Turn uint64
	Rule string
	Err  string `json:",omitempty"`
	// Reply is what the rule said back, if anything.
	Reply string `json:",omitempty"`
	// Confirmation is how the permission gate decided ("auto", "click",
	// "voice", "policy" or "timeout"). A refused rule has Err set and did
	// not run.
	Confirmation string `json:",omitempty"`
}

// ToolRan is published for each tool call the AI makes.
type ToolRan struct {
	Turn   uint64
	Name   string
	Args   json.RawMessage
	Result string
	// IsError is true when the tool failed, was refused or does not exist.
	IsError bool
	// Confirmation is how the permission gate decided ("auto", "click",
	// "voice", "policy" or "timeout"); empty for an unknown tool.
	Confirmation string `json:",omitempty"`
}

// Actors recorded in the action log (design §29): the owner until speaker
// recognition ships, and local API clients.
const (
	ActorOwner = "owner"
	ActorAPI   = "api"
)

// SettingChange is published when a setting changed, whoever changed it: by
// voice, through the local API, or by editing the config file.
type SettingChange struct {
	// Key is the dotted key; Old and New are the values in file form
	// (empty when unset). Secret values are references, never secrets.
	Key, Old, New string
	// Actor is ActorOwner or ActorAPI.
	Actor string
	// How it was confirmed: "voice", "click", or "auto" when no
	// confirmation was needed (the local API, a hand edit).
	How string
	// Reason says what led to it, such as "config.set" or "config file
	// edited".
	Reason string
}

// Reply is published with what Footman says back.
type Reply struct {
	Turn uint64
	Text string
}

// StopSource says what triggered a break-glass stop.
type StopSource string

// Stop sources (ADR-0017).
const (
	StopVoice  StopSource = "voice"  // "<wake word>, stop everything"
	StopHotkey StopSource = "hotkey" // the stop hotkey
	StopCall   StopSource = "call"   // Pipeline.Stop: tray, API (`system.stop_everything`)
)

// Stopped is published after a break-glass stop.
type Stopped struct {
	Source StopSource
	// Jobs lists the IDs of the jobs that were cancelled.
	Jobs []string
	// Turns is how many in-flight utterances (including any speech) were
	// cancelled.
	Turns int
}

// JobEvent is published for every update from a background job.
type JobEvent struct {
	ID     string
	Prompt string
	Update jobs.Update
}

// Failure is published when handling an utterance fails.
type Failure struct {
	Turn  uint64
	State State
	Err   string
}

// Topics the pipeline publishes on.
var (
	StateChanged = bus.NewTopic[StateChange]("pipeline.state")
	Transcribed  = bus.NewTopic[Transcript]("pipeline.transcript")
	RouteChosen  = bus.NewTopic[Routed]("pipeline.route")
	Inserted     = bus.NewTopic[Insertion]("pipeline.inserted")
	Acknowledged = bus.NewTopic[Acknowledgement]("pipeline.acknowledged")
	FastPathDone = bus.NewTopic[FastPathRan]("pipeline.fast_path")
	ToolCalled   = bus.NewTopic[ToolRan]("pipeline.tool")
	Replied      = bus.NewTopic[Reply]("pipeline.reply")
	StopDone     = bus.NewTopic[Stopped]("pipeline.stop")
	Failed       = bus.NewTopic[Failure]("pipeline.error")
	// MicChanged reports the microphone status, which the tray also shows
	// (design §29).
	MicChanged = bus.NewTopic[platform.TrayStatus]("pipeline.mic")
	// SettingChanged reports every setting change, for the action log.
	SettingChanged = bus.NewTopic[SettingChange]("settings.changed")
	// JobUpdated carries progress of background jobs.
	JobUpdated = bus.NewTopic[JobEvent]("jobs.update")
)
