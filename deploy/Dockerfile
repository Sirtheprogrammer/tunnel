# syntax=docker/dockerfile:1
#
# Builds tunnelxd (and the tunnelx client, in case you want it in the image
# for a quick smoke test) as static binaries, then copies just those binaries
# into a distroless runtime image. Every dependency in go.mod is pure Go
# (modernc.org/sqlite, certmagic, the yamux and cobra libraries), so
# CGO_ENABLED=0 works without pulling in a C toolchain anywhere in the build.
#
# Build:
#   docker build -f deploy/Dockerfile -t tunnelxd:latest --build-arg VERSION=$(git describe --tags --always) .
#
# Run (ACME mode; ports 80/443 map from the host to the unprivileged 8080/8443
# the process binds inside the container, since the distroless image runs as a
# non-root user that cannot bind ports below 1024):
#   docker run -d \
#     -p 80:8080 -p 443:8443 -p 7835:7835 \
#     -e CF_API_TOKEN=<cloudflare token with Zone:DNS:Edit> \
#     -v tunnelx-data:/var/lib/tunnelx \
#     tunnelxd:latest serve \
#       --domain tl.codesky.tech --tls-mode acme --acme-email you@codesky.tech \
#       --db /var/lib/tunnelx/tunnelx.db --acme-cache-dir /var/lib/tunnelx/acme-cache \
#       --http-addr :8080 --https-addr :8443

FROM golang:1.26-bookworm AS build
WORKDIR /src

# Cached separately from source: dependencies change far less often than code,
# so this layer usually stays warm across builds.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG VERSION=dev
ENV CGO_ENABLED=0
RUN go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/tunnelxd ./cmd/tunnelxd
RUN go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" -o /out/tunnelx  ./cmd/tunnelx
RUN mkdir -p /tmp/tunnelx-data

# distroless/static-debian12 carries a CA bundle (needed for the ACME and
# Cloudflare API HTTPS calls) and a built-in "nonroot" user, with no shell and
# no package manager -- there is nothing in it to exploit into a foothold.
FROM gcr.io/distroless/static-debian12:nonroot AS runtime
COPY --from=build /out/tunnelxd /usr/local/bin/tunnelxd
COPY --from=build /out/tunnelx  /usr/local/bin/tunnelx
COPY --from=build --chown=nonroot:nonroot /tmp/tunnelx-data /var/lib/tunnelx

# Holds the SQLite control-plane database and the ACME certificate cache.
# Mount a named volume or bind mount here so both survive a container restart.
VOLUME ["/var/lib/tunnelx"]

# 8080/8443: unprivileged stand-ins for 80/443, bound by --http-addr/--https-addr
# below. 7835: the agent control port, already unprivileged.
EXPOSE 8080 8443 7835

USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/tunnelxd"]

# A runnable default so `docker run tunnelxd:latest` shows something sensible;
# --acme-email and CF_API_TOKEN are per-deployment, so real usage overrides
# this CMD (see the run example above).
CMD ["serve", "--help"]
