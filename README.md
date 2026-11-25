# Footman

An open-source voice layer for your computer: clean dictation anywhere, and an assistant with a persona you choose ("Alfred, open YouTube"), who works in the background while you keep typing.

**Status:** design phase. See [docs/design.md](docs/design.md).

Contributing: see [CONTRIBUTING.md](CONTRIBUTING.md).

## Documentation

- [Users](docs/users/README.md): install, setup, everyday use, uninstall
- [Voices](docs/voices/README.md): making persona voices with the optional voice kit
- [Plugins](docs/plugins/README.md): writing plugins (manifest, MCP plus Footman extensions)
- [Contributing](docs/contributing/README.md): architecture, build, tests, ADRs
- Diagrams (Mermaid, rendered on GitHub):
  - [Architecture](docs/diagrams/architecture.md): the process model
  - [Pipeline](docs/diagrams/pipeline.md): the state machine from dictation to jobs
  - [Plugin handshake](docs/diagrams/plugin-handshake.md): core and plugin conversation
