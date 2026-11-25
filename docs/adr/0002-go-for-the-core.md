# 0002. Go for the core

- Status: Accepted
- Date: 2026-11-24

## Context
The product must be fast, small, and easy for non-developers to install on Linux, Windows, and macOS. Python was considered: its runtime overhead is small next to model inference, but its install story and footprint (interpreter, virtualenvs, PyTorch wheels) are poor.

## Decision
Write the product in **Go**: a single binary, about 20 MB of RAM at idle, simple cross-platform builds, and official SDKs for Anthropic, OpenAI, Gemini, and Ollama. Python is used only in the optional offline `voicekit/`.

## Consequences
Native speech libraries need cgo. Contributors need Go. Voice training stays in Python, where its tooling lives.
