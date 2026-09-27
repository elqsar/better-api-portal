# One image for both uses: the server (the default command, portal serve)
# and the CLI in CI (portal push, portal check). Build it with task image.
FROM docker.io/library/golang:1.26 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=devel
# GOFLAGS=-p=2 limits parallel compiles: vacuum's packages need more memory
# than a 4 GB podman machine has at full parallelism.
ARG GOFLAGS=
RUN --mount=type=cache,target=/root/.cache/go-build \
    GOFLAGS="${GOFLAGS}" CGO_ENABLED=0 go build -trimpath -buildvcs=false \
      -ldflags "-s -w -X main.version=${VERSION}" -o /portal ./cmd/portal

# Static, with CA certificates (OIDC discovery, token verification) and no
# shell; runs as an unprivileged user.
FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /portal /portal
COPY --from=build /src/internal/web/static/LICENSES.txt /LICENSES.txt
EXPOSE 8080
ENTRYPOINT ["/portal"]
CMD ["serve"]
