# Distribution

- **Module path** `github.com/elqsar/better-api-portal` (was
  `better-api-portal`), so `go install
  github.com/elqsar/better-api-portal/cmd/portal@vX` works once the repo
  is pushed there and public (or `GOPRIVATE` is set).
- **`portal version`** and `--version`: the `-X main.version` set by
  release builds, else the module version Go stamps (a pseudo-version
  like `v0.0.0-…-8c59a9b49a65+dirty` from a checkout, the tag after `go
  install …@vX`), plus Go version, platform and commit when known.
- **`task release -- vX`** (default `git describe`): `CGO_ENABLED=0`,
  `-trimpath -s -w`, linux and darwin × amd64 and arm64, each
  `dist/portal_<v>_<os>_<arch>.tar.gz` with the binary and `LICENSES.txt`
  (the vendored htmx and Scalar notices), plus `dist/SHA256SUMS`. About
  19 MB per archive. Publishing is manual: `gh release create vX dist/*`.
  Windows isn't built (no CI runner needs it yet).
- **Image** (`Containerfile`, `task image -- vX`): `golang:1.26` build
  stage to `gcr.io/distroless/static-debian12:nonroot` (CA certificates
  for OIDC, no shell, non-root), 69.6 MB. Entrypoint `/portal`, default
  command `serve`, so the same image runs the server or `portal push` in
  CI. `.containerignore` leaves out `.git`, `bin`, `dist`; the version
  comes from `--build-arg VERSION` (`-buildvcs=false`).
  - The default podman machine (3.7 GiB) killed the compiler at full
    parallelism (in vacuum's packages, after 30 min of thrashing), so
    `task image` passes `--build-arg GOFLAGS=-p=2`; the Containerfile
    also mounts a Go build cache. CI runners with more memory can leave
    `GOFLAGS` empty.
  - Checked: `version`, `check` on the mounted example, and `serve`
    against the dev database (`/readyz` 200, UI 503 without
    `oidc.issuer`, unauthenticated push 401).
- **Example workflow:** `PORTAL_CLI_VERSION` pinned in `env`; the job
  downloads the release archive for `$RUNNER_ARCH` and verifies it
  against `SHA256SUMS` before putting it on `PATH`. The install script
  was run against a local `dist/` over `file://`. `go install` and the
  image are given as alternatives in a comment.
- **Licence: Apache-2.0** (`LICENSE`, the Q8 default in 06-roadmap). The
  archives carry it next to `LICENSES.txt` (the vendored htmx and Scalar
  notices), and the image has both at `/`. Go module licences aren't
  collected into the archives yet.
