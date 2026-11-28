# 0051. Faster first spoken word: low effort, key and connection warm-up, early first clause

- Status: Accepted
- Date: 2026-11-28
- Amends: 0037

## Context
The latency line (ADR-0049) of a simple spoken question to the AI (Claude Haiku 5.5, streaming, web search offered, Kokoro on the CPU) read: end of speech to first text 2833 ms, to first audio 5343 ms. That is 2.7 s before the first token and 2.5 s more from the first token to the first sound. Reading the request and the speech path, offline, found these:

- **Thinking before the first token.** Haiku 5.5 runs adaptive thinking when the request does not set it, at effort `medium` unless told otherwise. The request set neither, so every voice reply started with thinking, which is output the user waits for and never hears.
- **The key came from the keyring on every request.** `Provider.Chat` called the key resolver each time, which on Linux is four Secret Service D-Bus calls (open session, search, maybe unlock, get secret) before the request could be sent. The connection to the API was also opened (TCP and TLS) only then.
- **Speech models unloaded for being idle (ADR-0028, `speech.unload_after`, 10 min) were reloaded only when the first sentence was ready to speak.** Speech-to-text had a `Hold` for push-to-talk; text-to-speech had nothing. Loading Kokoro again measured 0.7 to 1.1 s here with a warm file cache, and more with a cold one.
- **The first spoken chunk waited for the end of the first sentence** (the core splitter only cut at `.`, `!`, `?`, a line break or 200 characters), and the engine's own first-words cut (ADR-0037) then could only act on text it was handed whole. The engine also cuts a first sentence only at a comma, so a first sentence with no comma was synthesized whole. Kokoro takes about 0.15 s of CPU time per second of speech (8 threads), and nothing is heard until the first piece is done: measured first-audio time for one `Speak` call was 340 ms for 6 words, 507 ms for 14, 1027 ms for 23 and 1391 ms for 33 words with no comma.
- Not a cause: a cold first synthesis (160 ms for one word, no slower than later ones), the number of threads (8 is the default; 4 threads took 440 ms and 2 took 710 ms for the same 6 words), the system prompt and tools (already cached with `cache_control` on the system block and the last tool, and the permission gate is asked once per request), the player (`pacat` with a 20 ms buffer) and the join of sentence streams (gapless).

## Decision
- **Effort.** The Claude provider sends `output_config.effort` (new option `Effort`, config `ai.providers.claude.effort`: `low` (default), `medium`, `high`, `xhigh`, `max` or `default` to send none). It is sent only to models that take it (the Haiku 5, Sonnet 5, Opus 4.6 and later, Fable and Mythos families); Haiku 4.5 and unknown models get nothing, since it is an error on older ones. Thinking is left adaptive and is not switched off: `thinking: {type: "disabled"}` is a 400 on Sonnet 5.5 and Opus 5.5, and the guidance for Haiku 5.5 is to lower the effort rather than disable it. With `low` the model can still skip thinking on a simple request but a hard one still gets it. Effort is the same on every request, so the prompt cache is not disturbed.
- **Key in memory.** The resolved API key is kept for 5 minutes (`keyTTL`) and forgotten at once when the API answers 401 or 403. Failures are never kept, so a key stored after the daemon started is still found. The key is never logged and never part of an error.
- **Warm-up (`core.Warmer`).** When the wake word is heard, and when the command hotkey goes down, the pipeline starts, in the background: `Warm` on the text-to-speech engine (if a reply would be spoken), which loads the engine in use if it was unloaded and restarts its idle clock; and `Warm` on the AI provider, which resolves the key and sends one `HEAD` request to the API host with no credentials and no content, at most once in 30 s, so the TCP and TLS handshakes are done before the request. Dictation does not warm. The user speaks for seconds after the wake word, which is longer than either step takes.
- **First clause.** For a streamed AI reply, the splitter lets the first chunk end at a clause break (`,` `;` `:` `—` `–` followed by a space) once it has 4 words, if no sentence end comes first. Numbers (`1,000`), times (`7:45`) and code blocks are not cut. The chunk keeps its comma when spoken (`Speakable` trims it otherwise) so the voice pauses there. Text that is complete from the start (persona lines, fast-path replies) is not cut this way. Only the first chunk is.
- **First piece in the engine.** With no comma, the engine's first sentence is cut in front of its first connecting word (`and`, `but`, `or`, `so`, `because`, `which`, `that`, `who`, `while`, `when`, `where`, `as`, `with`, `then`, `if`, `though`, `although`, `since`) once it has `first_words` + 2 words and leaves at least three. No pause is inserted at such a cut, since it is in the middle of a phrase.

## Consequences
Measured offline (Kokoro, CPU, 8 threads, no API):
- First audio of a first sentence of 14 / 23 / 33 words with no comma: 507 / 1027 / 1391 ms before, 353 / 336 / 331 ms after the engine cut. For a first sentence with a comma the engine was already at about 340 ms.
- Reloading Kokoro after an idle unload: 0.7 to 1.1 s, now overlapping the speech instead of following it, whenever the engine was unloaded.

Estimated, not measurable without the real API (the effort figure is the one to check on the next journal line):
- Effort `low` instead of `medium` should shorten the time to first token by the length of the thinking the model no longer does, likely from several hundred ms to over a second for a simple question; this is the largest expected gain and has not been measured here.
- Key and connection warm-up: about 4 D-Bus round trips (tens of ms, more if the keyring has to unlock) and one TCP and TLS setup (roughly 100 to 300 ms) leave the path after the end of speech. A second request within 5 minutes saves the key lookup even without the wake word.
- First clause: the first audio starts after the first clause rather than the first sentence; the saving is the time the model takes to write the rest of that sentence (about 0.1 to 0.5 s for a 20-word sentence at typical streaming speeds, more when results of a web search arrive in bursts), plus the shorter first piece to synthesize.

Costs and limits:
- The wake word now causes one request to the API host with no credentials, including on a false wake (1 to 2 an hour, ADR-0041). It reveals only that the machine is online, which any request would. `HEAD` goes to `ai.providers.claude.url` when that is set.
- A rotated API key is picked up within 5 minutes, or at once after the first refused request.
- A first clause cut at a comma has no pause of its own beyond the comma's, since the engine pauses only inside one `Speak` call; the join of the sentence streams adds no gap. The first clause of a reply with short opening words ("Well, sir, ...") is not cut until it has 4 words.
- Effort `low` makes the model less likely to search the web or use a tool when unsure; `ai.providers.claude.effort = "medium"` restores the old behaviour for those who see that.

Follow-ups not done (riskier, or need the real API to judge):
- Switch thinking off for Haiku 5.5 (allowed at `low`, `medium` and `high`) for the voice path, and measure the first token with and without; per-turn effort by the kind of question.
- Leave `web_search` out of requests that clearly need no current information (a router or a cheap classifier); today the tool is offered to every request.
- Pre-warm the prompt cache with a `max_tokens: 0` request at the wake word, and use the 1-hour cache TTL: the 5-minute cache is cold for most voice requests, and a miss costs more than a hit (cache write 1.25x).
- Hold speech-to-text at the wake word as push-to-talk does, so a reload overlaps the speech; and report which speech models were unloaded in the latency line, so a slow turn can be told from a cold one.
- Stream the speech: Kokoro's callback delivers a whole piece at once. A first piece of 2 to 3 words would start sooner still, at some cost in prosody.
