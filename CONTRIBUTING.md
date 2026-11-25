# Contributing

## Workflow

1. Open (or pick) an issue.
2. Create a branch from `main` for it.
3. Open a pull request that says `Closes #N`. Don't commit directly to `main`.

## Decisions

Every decision change gets an ADR in `docs/adr/` (new, or one that supersedes an older ADR) and a matching update to `docs/design.md`, in the same PR.

## Tests

`go test ./...` must stay fast and self-contained: no GPU, network, desktop or API keys. Use fakes at interfaces. Slow or environment-dependent tests go behind build tags: `integration`, `gpu` or `live`, and run only when asked for (for example `go test -tags integration ./...`).

## Spikes

Spike code lives in `spikes/` and is never imported by the rest of the repo.

## Local tooling

`make build`, `make test`, `make lint` (needs [golangci-lint](https://golangci-lint.run/) v2) and `make cross`.
