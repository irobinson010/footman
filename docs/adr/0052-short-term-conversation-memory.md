# 0052. Short-term conversation memory

- Status: Accepted
- Date: 2026-11-28
- Amends: 0044, 0047

## Context
The owner asked Alfred for the best rated vacuums and got an answer, then said "Alfred, open links to show me" and Alfred did not know what links. Every new wake-word command started a new conversation: the pipeline kept the AI's messages only inside a follow-up window after a question (ADR-0047) and dropped them when the next command began. "Open those links" also never reached the AI, because the fast path read `open <anything>` as an application name.

Design §18 plans opt-in long-term memory ("what do you remember about me?") for a later sprint. This is the layer under it: what was said in the last few minutes.

## Decision

### 1. The AI remembers the last few exchanges across commands
An **exchange** is one request and its reply (the user message, any tool calls and results between, and the final answer). After each AI reply that Footman speaks or shows, the exchange is kept. The next request, whether from the wake word, push-to-talk, a typed command or a follow-up answer, is sent with the remembered exchanges first and the new message last.
- `ai.context.turns` (default `6`, `0` turns it off, live, voice-settable): how many exchanges are kept; the oldest go first.
- `ai.context.ttl` (default `10m`, `0s` never expires, live, voice-settable): the memory is forgotten when this long has passed since the last exchange, and each new exchange restarts the clock.
- **Memory only.** It is never written to disk, never logged, never put on the bus, and goes away when the daemon stops. The AI provider still receives it as part of each request, like any conversation.
- Requests that fail or end without an answer are not kept. A request handed to a background worker (`start_task`) is not kept either.
- The conversation a question leaves open for the follow-up window (ADR-0047) is now this memory: with `ai.context.turns` above zero the answer to "Which city?" finds the question there, and nothing more is stashed. With the memory off, the earlier follow-up-only behaviour is unchanged.

### 2. Old exchanges are compacted; the newest stays whole
The newest exchange is sent exactly as it happened, including the provider's raw blocks (for Claude web search, the encrypted results and citations, which must be replayed unchanged). When another exchange follows it, it is **compacted once, and stays compacted**: tool calls, tool results and raw blocks are dropped, and what is kept is the user's words, the words of the answer and a note of the pages it used:

`[Sources from that answer: 1. Best vacuums of 2026 (https://...); 2. ...]`

The pages come from a new `Sources` list on the LLM `Done` event (`llm.Source{Title, URL}`), which the Claude provider fills from the answer's citations first (the order the answer used them) and then the other search results, at most ten, http and https only, each once. The note keeps up to eight, with titles cut at 100 characters and flattened to one line. Compacting once, when the next exchange is stored, means the history prefix does not change from request to request after that.

### 3. Prompt caching is unchanged
The request is still the system prompt, then the tool definitions, then the messages, and the cache breakpoints stay on the system prompt and the last tool. History only adds messages after them, and none carries a breakpoint, so the cached prefix is the same with or without a memory. A test checks the placement. The new system prompt paragraph (below) is constant between requests.

### 4. Untrusted content stays untrusted (amends ADR-0044, ADR-0047)
Each remembered exchange records whether **it** brought in untrusted content (a web search, or a tool result marked untrusted). If any exchange still in the memory did, the next request starts with the untrusted flag set, so the permission gate asks before any Act, Outward or Destructive tool (ADR-0047): the AI asks for `open_url`, the owner is asked, a spoken yes answers it. When that exchange leaves the memory, by the turn limit, the ttl or "start over", the taint leaves with it. A compacted exchange keeps its flag: the titles and pages in its note are still web content.

### 5. Opening several pages takes one confirmation
`open_url` takes `url` or `urls` (up to five, both may be given). All addresses are checked before any page opens, and one invalid address opens none. One tool call is one confirmation: "Shall I open vacs.example, shark.example and dyson.example?", with the exact addresses in the click confirmation's detail, answered by one spoken yes or one click. Calling `open_url` three times would have asked three times.

### 6. "Open those links" reaches the AI
`open ...` is still a fast-path command, but a target that points back at an earlier answer ("those links", "the first two", "it", "them", "that page", "the second result") is no longer taken as an application name. The AI gets it, with the memory. `open firefox`, `open docs.example.org` and the site aliases are unchanged.

### 7. Telling the AI
When the memory is on, the system prompt says that earlier answers may be in the conversation, what a Sources line is, that links, pages and sources from an earlier answer are opened with a single `open_url` call carrying `urls`, that addresses are never read aloud, and that it must never write a Sources line itself.

### 8. Clearing it
- A spoken **"forget that"**, **"start over"**, **"start again"**, **"start fresh"**, **"new topic"**, **"new conversation"**, **"forget this conversation"**, **"clear the conversation"** (the whole utterance, "please" aside) clears the memory without calling the AI and answers "Okay, starting fresh." It is a fast-path rule of level Read: it only changes Footman's own state. `forget that` is also the phrase design §18 gives for deleting something from the planned long-term memory; when that lands, it should clear this memory and then ask what to forget, or take a more specific phrase.
- **A persona switch** (a different system prompt or voice) clears it, since the new persona did not say what the conversation holds. Setting the same persona again, as every config reload does, keeps it.
- **The break-glass stop** clears it.
- "Never mind" and "cancel" close a follow-up window or deny a confirmation but do not clear it: they end listening, not the topic. Closing the microphone does not clear it either. The ttl and the turn limit still apply.
- Changing `ai.context.turns` takes effect at once: fewer turns drop the oldest exchanges, zero forgets everything.

## Consequences
- Each request is larger by up to six short exchanges plus the full newest one, which after a web search holds the raw results (a few thousand tokens). It is sent only for ten minutes after the last request, and only the system prompt and tools are cached, so this part is billed as ordinary input.
- Taint now lasts as long as the exchange that caused it is remembered: for ten minutes after a web answer, any Act tool call, even for an unrelated request, asks for a confirmation (spoken yes works). "Start over" lifts it. This is the cost of carrying the page addresses forward; ADR-0047's rule is unchanged, only its reach in time.
- The memory is global to the daemon, not per person, because there is no speaker recognition yet. Anyone who says the wake word within ten minutes can refer to the last answer.
- Titles in a Sources note are web text. They are treated as untrusted data like the rest of the answer (the system prompt already says so) and are in a request only while their exchange is remembered.
- Another provider fills `Message.Sources` for its own search or leaves it empty: without it, an old exchange keeps only the words of the answer.
- The long-term memory of design §18 stays a separate, opt-in feature (`memory.enabled`).
