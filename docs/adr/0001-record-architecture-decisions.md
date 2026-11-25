# 0001. Record architecture decisions

- Status: Accepted
- Date: 2026-11-24

## Context
We need a durable record of why the project is built the way it is, for contributors and our future selves.

## Decision
Record every significant decision as an ADR in `docs/adr/`, numbered and never deleted. When a decision changes, write a new ADR that supersedes the old one and mark the old one `Superseded by NNNN`. `docs/design.md` describes the current design and links to the ADRs behind it.

## Consequences
Decisions are traceable. It costs a few minutes per decision.
