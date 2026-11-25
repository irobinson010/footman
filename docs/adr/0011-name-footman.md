# 0011. Name: Footman

- Status: Accepted
- Date: 2026-11-24

## Context
"Butler" and "Majordomo" collide with existing projects; Majordomo is an existing home-automation voice project.

## Decision
Name the project **Footman** (binary, repo, and module `footman`). A trademark search is still needed before the public launch.

## Consequences
PyPI and npm names are taken, which doesn't matter for a Go binary.

## Trademark search (2026-11-25)

This is a desktop-style knockout search done from a sandboxed cloud session, not a clearance search. **This is not legal advice.** The owner should confirm the items marked "not checked" before the public launch.

| Source | Query | Hits | Relevance |
|---|---|---|---|
| USPTO trademark search (tmsearch.uspto.gov) | `footman` | **Not checked.** Host blocked from this environment (DNS failure), and the tool needs JavaScript anyway | Unknown. Owner to run by hand (see below) |
| USPTO records via third-party mirrors (Markinton, Justia), found by web search | FOOTMAN, classes 9 and 42 | FOOTMAN Reg. 3309897, Marco Rigazio (Key Biscayne, FL), Class 42, registered Oct 2007. Listed as **dead** (post-registration filings in 2013, mirror updated Jan 2026) | Same class as the project, but dead. Goods not verified |
| Same | FOOTMAN, other classes | Reg. 0502685 Knomark Manufacturing (shoe shine kit), dead. Reg. 1103498 Scott Paper (floor coverings, Class 27), dead | None. Dead and unrelated goods |
| Same | Near marks | DRFOOTMAN Reg. 7300720 (Class 35, live). FOOTIMAN Serial 90494482 (Class 28, pending) | Low. Different marks, classes and goods |
| EUIPO / TMview (tmdn.org) | `footman` | **Not checked.** Host blocked from this environment | Unknown. Owner to run by hand |
| Web search | Footman AI assistant / voice dictation / app | No product named Footman found among dictation and assistant apps (Wispr Flow, Super Voice Mode and others) | None found |
| App stores (via web search) | Footman AI agent / virtual assistant app | No Footman-named app surfaced. Store search pages were not queried directly | None found, weak evidence |
| GitHub repository search (name contains `footman`) | `footman in:name` | `maxvitek/footman`: voice-activated home automation, Python, 3 stars. `willemkokke/footman`: Python task runner (archived, 3 stars). `meck/alfred-footman`: Haskell library, 10 stars. About a dozen other empty or hobby repos (a football game, a crypto wallet generator, others) | The home-automation voice project is nearest in topic but tiny and unregistered. The task runner may also install a `footman` command |

### Searches for the owner to run by hand
- USPTO: https://tmsearch.uspto.gov, choose "Search by: Basic", query `footman`, then repeat with `*footman*` and `foot man`. Filter on live marks and classes 9, 42 and 35. For any hit, open the TSDR status page and read the goods and status. Also confirm Reg. 3309897 is dead and read its goods.
- EUIPO/TMview: https://www.tmdn.org/tmview, trademark name `footman`, offices EUIPO, UKIPO and WIPO, classes 9 and 42.
- Also worth running: WIPO Global Brand Database (https://branddb.wipo.int), query `footman`.
- Package names: check whether the `footman` command or name is already installed by the PyPI task runner, which could confuse users sharing a PATH.

## Risk assessment

**Low, with caveats.** Reasoning:
- No live FOOTMAN mark in class 9 or 42 turned up. The only class 42 FOOTMAN registration found is listed as dead, so it is not a bar on its own, though a lapsed owner could still have common-law rights if they kept using the name.
- "Footman" is an ordinary English word (a household servant), so it is a suggestive fit for an assistant. Marks like this are weak and hard to monopolise, but it also means others may use it.
- Footman is non-commercial and open source, which lowers practical exposure.
- Caveats: the USPTO and EU registers were not searched directly, only third-party mirrors, so pending applications and recent filings could be missing. Common-law (unregistered) use is only partly covered by web search. The existing small GitHub projects, including a voice-activated home-automation one, show some name overlap and possible user confusion, but none appears to hold registered rights.

## Recommendation

Keep the name **Footman**. Use a descriptor in public-facing copy (for example "Footman, an open-source voice assistant") to separate it from the other projects and to leave room to adopt a qualifier if a conflict appears. Before the public launch, run the USPTO and TMview searches above. If a live class 9 or 42 mark for voice-assistant or dictation software turns up, reopen this ADR and consider a qualifier such as "Footman Voice" or a rename. Status stays **Accepted**.
