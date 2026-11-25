# Pipeline

The state machine from [design §5](../design.md#5-pipeline). A wake word or hotkey starts a turn. Dictation is inserted at the cursor. Commands get a fast path or the AI with tools. Long tasks move to the job manager, and the pipeline returns to idle at once.

*Figure: the break-glass stop ("Alfred, stop everything", wake word required, or the hotkey or tray icon) cancels jobs, stops speech and mutes the mic from any active state.*

```mermaid
stateDiagram-v2
    [*] --> idle

    state "Listening" as listening
    state "Transcribing" as transcribing
    state "Routing" as routing
    state "Clean up" as cleanup
    state "Insert at cursor" as insert
    state "Acknowledgement clip" as ack
    state "Fast path" as fastPath
    state "AI and tools" as aiTools
    state "Reply" as reply
    state "Job queued" as jobQueued
    state "Stopping" as stopping

    idle --> listening: wake word or hotkey
    listening --> transcribing: end of speech
    transcribing --> routing

    routing --> cleanup: dictation
    cleanup --> insert
    insert --> idle

    routing --> ack: command
    ack --> fastPath: simple command
    ack --> aiTools: needs AI
    fastPath --> reply
    aiTools --> reply: short task
    reply --> idle

    aiTools --> jobQueued: long task
    jobQueued --> idle: returns to idle at once

    listening --> stopping: break-glass stop
    transcribing --> stopping: break-glass stop
    routing --> stopping: break-glass stop
    aiTools --> stopping: break-glass stop
    reply --> stopping: break-glass stop
    jobQueued --> stopping: break-glass stop
    stopping --> idle: cancel jobs, stop speech, mute mic
```
