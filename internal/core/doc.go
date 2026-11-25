// Package core holds the pipeline state machine (design §5) and the
// simple-tier router (design §17). It is the heart of the daemon and depends
// only on interfaces, never on a concrete platform, speech or LLM
// implementation.
//
// An utterance moves through the pipeline like this:
//
//	idle ─wake word / hotkey─► listening ─end of speech─► transcribing ─► routing
//	routing ─dictation─► cleaning ─► inserting ─► idle
//	routing ─command──► acknowledging ─► fast path │ thinking ⇄ tool ─► replying ─► idle
//	                                                └─ long task ─► jobs.Worker ─► idle (at once)
//
// Every state change is published on the bus as a StateChange on
// StateChanged, together with the other events in events.go.
//
// The break-glass stop ("<wake word>, stop everything", ADR-0017) is checked
// after transcription and before routing, so it works whatever else is going
// on, including while another utterance is being handled. Pipeline.Stop does
// the same for the hotkey and the tray.
package core
