# Plugin HTTP Example

This example shows how to run local HTTP-backed plugins for testing.

The plugin bundles live in `_testing/plugins/` as repository-local examples and fixtures.
They are not the core agent runtime.
The actual plugin runtime executables live separately under `_testing/runtimes/`.

## Start the plugin runtimes

```bash
go run ./_testing/runtimes/send-email-plugin
```

In a second terminal:

```bash
go run ./_testing/runtimes/web-research-plugin
```

Default listen addresses:

- `send-email-plugin`: `127.0.0.1:8091`
- `web-research-plugin`: `127.0.0.1:8092`

## Install the example plugins

```bash
HOME=/tmp/agent-home go run ./cmd/agent plugins install ./_testing/plugins/send-email --link
HOME=/tmp/agent-home go run ./cmd/agent plugins install ./_testing/plugins/web-research --link
```

## Create a temporary config

You can still copy `docs/examples/config/plugin-http-example.yaml` to `~/.agent/config.yaml`, but the easier path now is to use the plugin config CLI against a temporary `HOME`.

Create the config directory and set the required keys:

```bash
mkdir -p /tmp/agent-home/.agent
HOME=/tmp/agent-home go run ./cmd/agent plugins config set send-email provider smtp
HOME=/tmp/agent-home go run ./cmd/agent plugins config set send-email baseURL http://127.0.0.1:8091
HOME=/tmp/agent-home go run ./cmd/agent plugins config set send-email smtpHost mail.privateemail.com
HOME=/tmp/agent-home go run ./cmd/agent plugins config set send-email smtpPort 465
HOME=/tmp/agent-home go run ./cmd/agent plugins config set send-email username support@example.com
HOME=/tmp/agent-home go run ./cmd/agent plugins config set send-email password 'CHANGE_ME'
HOME=/tmp/agent-home go run ./cmd/agent plugins config set send-email from support@example.com
HOME=/tmp/agent-home go run ./cmd/agent plugins config set web-research provider internal
HOME=/tmp/agent-home go run ./cmd/agent plugins config set web-research baseURL http://127.0.0.1:8092
HOME=/tmp/agent-home go run ./cmd/agent plugins config set web-research apiKey local-test-key
HOME=/tmp/agent-home go run ./cmd/agent plugins config send-email
HOME=/tmp/agent-home go run ./cmd/agent plugins validate-config send-email
HOME=/tmp/agent-home go run ./cmd/agent plugins validate-config web-research
HOME=/tmp/agent-home go run ./cmd/agent plugins enable send-email
HOME=/tmp/agent-home go run ./cmd/agent plugins enable web-research
HOME=/tmp/agent-home go run ./cmd/agent plugins list
```

If you prefer a fixed example file, `docs/examples/config/plugin-http-example.yaml` remains a reference config.

## Test the web plugin

```bash
HOME=/tmp/agent-home go run ./cmd/agent run --profile ./_testing/profiles/web-local/profile.yaml "search golang plugin architecture"
```

## Test the email plugin

```bash
HOME=/tmp/agent-home go run ./cmd/agent run --profile ./_testing/profiles/email-local/profile.yaml --approval always "send email hello from the local plugin server"
```

Note:

- `email/send` is treated as a sensitive plugin action
- the policy layer now requires approval for it even when it is available in the profile
- use `--approval always` only for controlled testing
- shell and network decisions can also be overridden explicitly with profile policy overlays when you really want non-interactive automation

If you want to test an explicit override, use `./_testing/profiles/email-openai-no-approval/profile.yaml`.
That profile demonstrates a policy overlay that allows `email/send` without an approval prompt.

## Expected behavior

- `web-local` uses the `mock` provider to call `web/search` or `web/fetch`
- `email-local` uses the `mock` provider to call `email/draft` or `email/send`
- the actual tool execution is handled by separate HTTP plugin runtimes in `_testing/runtimes/`, not the core `agent` binary
