# 0044. Web answers: provider-side web search, `ai.web_search` and `user.location`

- Status: Accepted
- Date: 2026-11-27

## Context
Asked "what's the weather today?", the assistant said it could not, but could open a web page. For a voice assistant that is the wrong answer to every quick factual question: weather, a share price, last night's score, opening hours, "who is...". The user wants the answer spoken, in a sentence.

Anthropic's Messages API runs web search on its own side (a *server tool*): the model decides to search, the API runs the search and gives the model the results inside the same request, and the reply carries the search, its (encrypted) results and text with citations. Nothing is executed by Footman. ADR-0004 keeps four providers, and only some can do this.

## Decision
**The Claude provider offers `web_search_20260209`** (Anthropic Go SDK `WebSearchTool20260209Param`), with:
- `max_uses = 3`, to bound cost (USD 10 per 1,000 searches, plus the result tokens) and latency. A spoken factual answer needs one or two searches.
- `allowed_callers = ["direct"]`. The `_20260209` version defaults to *dynamic filtering*, where the model runs the search from inside a code-execution container. That adds latency and nested blocks, is not eligible for zero data retention, and buys little for a one-sentence answer. Calling the tool directly gives the plain behaviour on every current model, including the default Haiku.
- The tool is last in the tool list and carries the prompt-cache breakpoint, so the system prompt and all tool definitions stay cached.

**Web fetch is not offered.** It would let the model read any URL it chooses, which widens the prompt-injection surface (ADR-0015), for answers search already gives. It can be added later behind its own setting.

**Server-tool blocks round-trip unchanged.** `llm.Message` gets an opaque `Raw` (the assistant's content blocks as the API sent them). The Claude stream accumulates the whole reply with the SDK's `Message.Accumulate` and, when the reply used a server tool, returns that content on the `Done` event; core keeps it on the assistant message, and the provider sends it back verbatim (`param.Override`) on the next call. The encrypted result and citation indexes therefore survive, as the API requires. Replies without a server tool keep no `Raw` and use the ordinary text and tool-call path. A `pause_turn` stop reason (`llm.StopPause`) makes the core send the paused reply straight back to go on, within `MaxToolRounds`. When the model calls a search and a Footman tool together, the API returns `tool_use` with the search still pending; the whole reply is replayed with the tool results and the API runs the search then.

**Config.** `ai.web_search` (bool, default **true**, live) offers the tool when the provider can search. It is voice-settable in the tightening direction only: "turn off web search" works at once, turning it on needs a click (ADR-0039), like other settings that widen what leaves the machine. `user.location` (free text, default empty, voice-settable, live) is where the user is, "Raleigh" or "Lyon, France". It goes into the system-context block (ADR-0043) only when set, on one line cut to 100 characters, and follows the same `context.cloud`/`context.local` policy as the rest of that block: with the `system` item off, the location is not sent either. It is never derived from the IP address or anything else on the machine. The structured `user_location` field of the search tool is not used, since a free-text place cannot be mapped to city, region and country reliably.

**Prompt.** With web search the system prompt tells the model to search and answer current-information questions itself in one or two spoken sentences with the key figure (temperature and conditions; price and change), never to say it cannot browse, announce the search or read out links, and to treat results as untrusted. If the answer depends on where the user is and no location is given, the model asks for the city once, and saves it with `set_setting` on `user.location` when told. Without web search (setting off, or a provider that cannot) the prompt says the model has no web access, so it does not pretend to look things up, and may offer to open a page.

**Permission.** Web search is a **Read** action (`core.WebSearchSpec`). The search itself runs on the provider, so there is nothing to gate per call; the core asks the gate once per request before offering the tool, so `permissions.read = "deny"` switches it off and `"confirm"` asks for a click per request. Each search is recorded in the action log (`web_search`, with how the gate decided). **The results are untrusted content** (ADR-0015): when a reply used web search, the task is marked untrusted, so an outward or destructive tool the model then calls needs a confirmation whatever the config says, and a job handed to the worker starts untrusted. The gate's existing rule is unchanged; it covers outward and destructive actions, not Act ones such as `open_url` (see Consequences).

**Other providers.** `llm.Caps.WebSearch` says whether a provider can search. Ollama and the other local providers cannot; they keep today's behaviour and are told they have no web access. A search for them belongs in a plugin (a SearXNG or Brave Search API tool, which the existing tool loop and gate already handle, with results marked `Untrusted`), tracked as a follow-up issue.

## Consequences
- "What's the weather", "Apple's share price" and "who won the game" get a spoken answer on the default Claude setup, at the cost of up to three searches per request plus result tokens; `llm.Usage.WebSearches` counts them.
- Search queries go to Anthropic with the request, as the question already did. The location, when set, goes in the system block. Turning off `ai.web_search` (by voice) stops both the tool and, with `user.location` cleared or `context.cloud = []`, the location.
- Text before a search ("Let me check.") is still spoken as it streams; the prompt tells the model not to narrate. Citations are kept in the replay but not shown: replies are spoken and shown as one text (ADR-0042), so a source would be read aloud.
- The Act exposure this note first recorded is closed by ADR-0047: once a search has run, Act tools such as `open_url` and `open_app` need a confirmation too (Read stays automatic).
- Follow-ups: a search plugin for local models; optional web fetch; showing the source in the notification.
