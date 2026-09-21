# Tough Decisions Council (council) — multi-stage image.
#
# Build:
#   docker build -t council:local .
#
# Run (SQLite DB + reports on one writable volume, config/casepack mounted read-only):
#   mkdir -p data
#   docker run --rm -p 8080:8080 \
#     -v "$PWD/data:/data" \
#     -v "$PWD/config:/config:ro" \
#     -v "$PWD/casepack:/casepack:ro" \
#     -e COUNCIL_DB_PATH=/data/council.db \
#     -e COUNCIL_REPORTS_DIR=/data/reports \
#     -e COUNCIL_GATEWAY_BASE_URL=http://host.docker.internal:4000 \
#     council:local
#
# First-run bootstrap on the same volume:
#   docker run --rm -v "$PWD/data:/data" -v "$PWD/config:/config:ro" \
#     council:local db migrate
#   docker run --rm -v "$PWD/data:/data" -v "$PWD/config:/config:ro" \
#     council:local pack init --file /config/pack.yaml
#
# Secrets (COUNCIL_GATEWAY_API_KEY) are injected at runtime via -e/--env-file and
# are never baked into the image.

FROM golang:1.25 AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
# CGO is disabled so the binary is fully static (SQLite driver is pure Go).
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags "-s -w" -o /out/council ./cmd/council

# The runtime image is distroless: no shell, so it cannot mkdir/chown at startup.
# Create the writable dirs here and hand them over to 65532 (= the `nonroot`
# uid/gid used by the distroless image) via --chown on the COPY below. Without
# this, a Docker-managed volume seeded from /data would be root-owned and the
# CLI could not create /data/council.db on first run.
RUN mkdir -p /data/reports && chown -R 65532:65532 /data

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/council /usr/local/bin/council
COPY --from=build --chown=65532:65532 /data /data

# SQLite database + generated reports live here; mount a volume for persistence.
# A named volume inherits the ownership copied above, but a *bind mount* uses the
# host directory's own ownership, so `chown -R 65532:65532 <host-dir>` is still
# required on first run (or a host dir already owned by the runtime user).
VOLUME ["/data"]

ENV COUNCIL_DB_PATH=/data/council.db \
    COUNCIL_REPORTS_DIR=/data/reports

USER nonroot:nonroot

ENTRYPOINT ["/usr/local/bin/council"]
CMD ["serve"]
