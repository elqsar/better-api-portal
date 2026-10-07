# Push client

- **PR jobs use `push --dry-run`** in the example workflow, not
  `check --baseline-from`: it's the server's exact verdict, org rules
  included (`descriptor-owner-unknown` needs the config the service repo
  doesn't have). `check --baseline-from` is for local runs and J2's
  "offline apart from one fetch".
- **Credentials:** `PORTAL_TOKEN` wins, sent as is (so GitLab's `id_tokens`
  named `PORTAL_TOKEN` just works); else GitHub Actions' ID token for
  `--audience`. The run headers (`X-Portal-Ref`, …) are sent with static
  tokens only.
- **Retries:** network errors and 5xx, three times (1s, 2s, 4s); 4xx never.
  A 401 message hints at `id-token: write` / `PORTAL_TOKEN`.
- **Local failures first:** a descriptor or `$ref` closure with errors is
  reported without contacting the portal (exit 1).
- **Paths:** the server's descriptor-relative paths are joined with the
  descriptor's directory, so text and SARIF match `check`'s; the JSON output
  is the push response, with status and URL per API.
- `--baseline-from` skips AsyncAPI APIs, logs each baseline to stderr, and
  verifies the bundle against `X-Portal-Content-Hash`.
