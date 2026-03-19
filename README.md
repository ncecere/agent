# Agent

`github.com/ncecere/agent` is a local-first Go agent framework with:

- a generic runtime
- loaded profiles
- installable plugins
- a small built-in core tool set: `read`, `write`, `edit`, `bash`, `glob`, `grep`
- policy and approval gates
- SQLite-backed sessions and resume
- external integration paths through `http`, `host`, and `mcp` plugin runtimes

## Status

The current codebase is in a solid `v0.1` state:

- `go test ./...` passes
- the core runtime, CLI, profiles, plugins, sessions, policy, and approvals all work together
- OpenAI-compatible providers, separated plugin runtimes, sub-agent spawning, and MCP client support are implemented

## Repository layout

- `cmd/agent` - the core CLI host
- `internal/` - runtime, services, providers, registries, persistence, policies
- `pkg/` - public framework contracts
- `docs/` - plans, workflow docs, examples, and bridge documentation
- `_testing/` - repository-local profiles, plugins, runtimes, and fixtures for development/testing

The `_testing/` directory is not the core runtime. It exists to provide local examples and fixtures while developing the framework.

## Quick start

Run the CLI:

```bash
go run ./cmd/agent
```

Common commands:

```bash
go run ./cmd/agent run --profile ./_testing/profiles/readonly/profile.yaml "hello"
go run ./cmd/agent chat --profile ./_testing/profiles/coding/profile.yaml
go run ./cmd/agent sessions list
go run ./cmd/agent plugins list
```

## Plugin workflow

Install a local plugin bundle:

```bash
go run ./cmd/agent plugins install ./_testing/plugins/send-email --link
```

Configure it from the CLI:

```bash
go run ./cmd/agent plugins config set send-email provider smtp
go run ./cmd/agent plugins config set send-email baseURL http://127.0.0.1:8091
go run ./cmd/agent plugins validate-config send-email
go run ./cmd/agent plugins enable send-email
```

More details:

- `docs/plugins.md`
- `docs/plugin-http-example.md`
- `docs/mcp-bridge.md`

## Architecture docs

Planning and architecture notes live under `docs/architecture/plans/`.

Useful entry points:

- `docs/architecture/plans/go-agent-framework-plan.md`
- `docs/architecture/plans/go-agent-framework-package-layout.md`
- `docs/architecture/plans/go-agent-framework-v0.1-feature-list.md`
- `docs/architecture/plans/go-agent-framework-plugin-spec.md`

## Release checklist

Before tagging a `v0.1` release, verify:

- `go test ./...`
- `go run ./cmd/agent doctor`
- one provider-backed run works end to end
- one plugin-backed tool works end to end
- sessions can be created, listed, resumed, and exported

Additional release docs:

- `docs/release-checklist-v0.1.md`
- `CHANGELOG.md`

## Notes

- The framework is currently an MCP client, not an MCP server.
- Integration-specific code should stay out of the core runtime and live in plugin runtimes or external tool servers.
