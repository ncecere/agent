# v0.1 Release Checklist

Use this checklist before tagging the first `v0.1` release.

## Core validation

- [ ] `go test ./...`
- [ ] `go run ./cmd/agent doctor`
- [ ] one `run` flow works with the mock provider
- [ ] one `run` flow works with the OpenAI-compatible provider
- [ ] one plugin-backed tool works end to end
- [ ] one approval-gated action is blocked or approved correctly
- [ ] one session can be created, listed, resumed, and exported

## CLI validation

- [ ] `agent run --profile <path>` works
- [ ] `agent chat --profile <path>` works
- [ ] `agent resume` works
- [ ] `agent profiles list/show/validate` works
- [ ] `agent plugins list/show/validate` works
- [ ] `agent plugins install/enable/disable/remove` works
- [ ] `agent plugins config/config set/config unset/validate-config` works
- [ ] `agent sessions list/show/export` works
- [ ] `agent config show/paths` works

## Plugin validation

- [ ] HTTP plugin runtime works
- [ ] command plugin runtime works
- [ ] host plugin runtime works (`spawn-sub-agent`)
- [ ] MCP client bridge works against at least one MCP server
- [ ] sensitive plugin actions require approval by default
- [ ] policy overlays can explicitly override a sensitive tool decision

## Docs validation

- [ ] `README.md` matches the current repo structure
- [ ] `docs/plugins.md` matches current CLI behavior
- [ ] `docs/building-plugins.md` matches current runtime behavior
- [ ] `docs/plugin-runtime-choices.md` matches the implemented runtimes
- [ ] `docs/examples/build-a-web-research-plugin.md` still works as written
- [ ] `docs/examples/build-a-send-email-plugin.md` still works as written
- [ ] `docs/plugin-author-checklist.md` matches the current plugin model
- [ ] `docs/plugin-http-example.md` still works as written
- [ ] `docs/mcp-bridge.md` matches the implemented bridge model

## Repository hygiene

- [ ] no secrets are committed intentionally
- [ ] example `_testing/` assets are clearly separated from core code
- [ ] `go.mod` module path is correct
- [ ] release notes are updated in `CHANGELOG.md`

## Tagging

- [ ] decide release tag name (recommended: `v0.1.0`)
- [ ] create tag after final verification
- [ ] publish release notes based on `CHANGELOG.md`
