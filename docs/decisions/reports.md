# Reports

- **JUnit failures match the exit code.** Errors are failures, and warnings
  are too only with `--strict`. Other findings are passing test cases, with
  their line in `<system-out>`. Each API also gets a passing
  `version … score …` case listing its changes.
- **SARIF locations:**
  - Paths are relative to the working directory (`%SRCROOT%`), so run
    `check` from the repository root.
  - A file outside it, such as a baseline's, is replaced by the descriptor
    (line 1), with "(at file:line, outside the checkout)" in the message.
  - If the descriptor is outside it too, locations are absolute `file://`
    URIs.
  - `partialFingerprints["portal/v1"]` ignores line numbers.
  - The output validates against the official 2.1.0 schema; checked by hand,
    not in the tests, which would need the network.
- `--output` values are validated before the run, and the files are written
  even when the check fails.
