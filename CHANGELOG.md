# Changelog

## v0.1.0

Initial framework release.

### Added

- generic Go agent runtime with typed events
- CLI host with `run`, `chat`, `resume`, profile, plugin, session, config, and doctor commands
- built-in core tools: `read`, `write`, `edit`, `bash`, `glob`, `grep`
- OpenAI-compatible provider with chat/responses support and SSE streaming fallback handling
- SQLite-backed session persistence, resume, export, and structured transcript replay
- declarative profile loading with policy overlays
- installable plugin system with local discovery and lifecycle commands
- plugin config management from the CLI
- HTTP plugin runtime execution
- host plugin runtime support with `spawn-sub-agent`
- MCP client bridge for external MCP tool servers
- approval and policy enforcement for risky actions and sensitive plugin tools

### Notes

- `_testing/` contains repository-local example plugins, profiles, runtimes, and fixtures
- `docs/` contains user-facing guides and architecture plans
- the framework acts as an MCP client, not an MCP server
