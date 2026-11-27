# User documentation

This section is for people who install and use Footman: dictating text anywhere on your computer and talking to your assistant. You don't need to read about voice training, plugins or the internals to do that.

## Hands-free dictation

You don't need to hold a key. Say your assistant's name and one of these, and everything you say next is typed into the app you are in, until you stop it:

| To start | Say |
|---|---|
| Type into the current app | "Alfred, write", "Alfred, dictate", "Alfred, take a note" or "Alfred, start dictation" |
| Start a new note | "Alfred, write in a new note" (a `.txt` file) or "Alfred, write in a new document" (a `.md` file) in `~/Documents/Footman notes`, opened in your default editor |
| Continue in a file | "Alfred, open meeting notes and write". If several files match, Alfred reads out the top three; say "one", "two" or "three" (or "cancel") |

Use your own assistant's name (Alfred, Klaus, …). "Alfred, write hello" with words after it still types just "hello" and stops.

While dictating, a notification and the tray icon show that the microphone is live. To stop, say "stop dictation", "end dictation", "that's all" or "Alfred, stop" (as a whole sentence, so "stop dictation errors in the log" is typed as text), or say nothing for 60 seconds. "Stop everything" with the wake word stops it too. You can change the phrases (`dictation.stop_phrases`, also by voice) and the timeout (`dictation.idle_timeout`).

While dictating you can also say, as a whole sentence on its own: "new line", "new paragraph", "scratch that" (or "delete that"), "go to the end", "go to the start", "end of line", "start of line", "select all", "undo", "redo", "press enter", "press tab", "press escape" and "save". In a terminal Footman never presses Enter, even if you say "press enter", and leaves out undo, redo, save and select all; end of line, start of line, tab and escape still work. Details: [ADR-0045](../adr/0045-voice-started-dictation.md).

## Coming soon

- Install: downloading the binary or running the install script
- Setup: `footman setup`, choosing a provider and a persona, and storing keys
- Everyday use: dictation, commands, personas, and the break-glass stop
- Uninstall: `footman uninstall` and the choices it offers for keeping config and data

For the full design in the meantime, see [docs/design.md](../design.md), especially §11 (install and uninstall).
