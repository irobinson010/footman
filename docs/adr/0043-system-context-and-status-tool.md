# 0043. The AI knows the machine: "system" context item on by default, read-only system_status tool

- Status: Accepted
- Date: 2026-11-26

## Context
Asked "what process on my computer is using the most memory right now?", the persona answered with how Windows, macOS and Linux each show it. The command agent knew nothing about the computer it runs on, and had no tool to look. Footman is an assistant on one particular machine (design §1); generic multi-OS advice is the wrong answer to almost every question about "my computer".

What the AI may see besides the user's words is governed by `context.local` and `context.cloud` (design §18, §28), whose items so far (`window_title`, `clipboard`, `selection`) are off by default and loosened only by a click (ADR-0039).

## Decision
**A new context item, `system`.** With it, the AI's system prompt carries a block of coarse facts about the computer: OS, distribution and version, kernel, desktop and session type (Wayland/X11), login shell, CPU model with cores and threads, installed RAM, graphics adapters, locale, time zone, and the platform Footman runs on (`linux/amd64`). Each request also carries the local date and time. `all` covers `system` like any other item, and the ADR-0039 rules are unchanged: dropping `system` tightens (by voice, at once), adding it loosens (a click).

**On by default, for both `context.local` and `context.cloud`.** The block is machine information, not personal content: no host name, user name, paths, window titles, file names, network addresses or anything the user typed. Much of it is what any web site learns from a browser's user agent and `Intl` settings (OS, locale, time zone, CPU threads), and the rest (CPU and GPU model, RAM size) describes millions of identical machines. Without it the assistant gives wrong or useless answers to ordinary questions, which is the failure that prompted this. The other items stay off by default: they carry what the user is doing. Turning it off is one sentence ("stop telling the cloud about my system", a tightening) or `context.cloud = []`.

**Where it goes in the prompt.** The facts follow the persona's style and CoreRules in the system prompt. They are collected once when the daemon starts, so the system prompt (and the provider's prompt cache over it and the tools) is identical from one request to the next. The date and time change every minute, so they go at the head of the user turn (`[Local time: Thursday 26 November 2026, 14:05 EST]`), outside the cached prefix. The block is switched on and off live with config reloads; which list applies depends on the provider (`context.local` for local ones such as Ollama, `context.cloud` otherwise, including when unsure).

**How it is collected.** `platform.SystemInfo` is an optional extension of `Platform`. On Linux, facts come from `/etc/os-release`, `/proc`, the session's environment and one `lspci -mm` run (or the NVIDIA driver's `/proc` entries), cached. A platform without `SystemInfo` gives the portable basics from the Go runtime and environment (`platform.BasicFacts`).

**A read-only `system_status` tool** (permission `read`, through the permission gate like every tool, so it runs without asking under the default policy). It reports CPU busy, load and uptime; memory and swap; the top 10 processes by memory and by CPU and the top 5 process names by summed memory; free space on local filesystems; batteries; and NVIDIA GPU memory, load and temperature when `nvidia-smi` is installed. On Linux it reads `/proc` and `/sys` directly (no `ps`), sampling CPU twice half a second apart. It reports **process names (`comm`) and PIDs only, never command lines**, which can hold tokens and passwords. Its result is marked untrusted, since any program can name its own process; an outward or destructive action later in the same task then needs confirmation (design §29). The action log redacts it like any tool result. The tool is offered only when the platform implements `SystemInfo`.

CoreRules now tell the model to answer for this computer, not explain other operating systems, and to look things up with its tools instead of telling the user how to check.

## Consequences
- "What's using my memory?" gets an answer about this machine, from real numbers.
- Existing installs whose config file sets `context.cloud` explicitly keep their list; only those using the default gain `system`. The design's sample config now shows `["system"]`.
- `system_status` is not tied to the `system` context item: it is an action the AI takes, gated by the `read` permission like other tools, not context attached to every request. A user who wants the AI to see nothing about the machine sets `permissions.read` stricter or removes `system`; whether the tool should also follow the context item is left open.
- More tools of this kind (listing services, network status, killing a process at `destructive`) can follow the same pattern.
- Windows and macOS implement `SystemInfo` in Sprint 10; until then they would give the basics only.
