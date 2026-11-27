# 0015. Security: confirmations and untrusted content

- Status: Accepted
- Date: 2026-11-24

## Context
Voice can come from anyone or from played-back audio, and the AI reads untrusted content (web pages, messages, files).

## Decision
(1) Security-sensitive changes need a **physical click**, never voice. (2) Unknown speakers need a **click to allow** (once speaker recognition ships). (3) After untrusted content enters a task's context, **every outward or destructive action in that task needs confirmation**, whatever the config says. (4) A break-glass stop is always available (see ADR-0016).

## Consequences
Some extra clicks, in exchange for strong protection against spoofing and prompt injection.

## Amendment (ADR-0047)
Rule (3) now covers **Act** actions too: once untrusted content is in a task, every tool call except a Read one needs confirmation. A spoken yes can answer an Act or Outward confirmation; a Destructive one, and security-sensitive changes (rule 1), still need the physical click.
