# 0004. Four LLM providers

- Status: Accepted
- Date: 2026-11-24

## Context
Users have different subscriptions, budgets, and privacy needs.

## Decision
Support **Claude** (primary), **OpenAI**, **Gemini**, and **Ollama** behind one `Provider` interface. Heavy jobs go to **worker agents**: a built-in loop, Claude Code, or Codex.

## Consequences
More adapters to maintain. Offline and local-first modes become possible.
