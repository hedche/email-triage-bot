# Multi-stage build. The builder runs on the native platform and cross-compiles
# with GOOS/GOARCH from buildx, which is far faster than emulating arm64.
FROM --platform=${BUILDPLATFORM} golang:1.27.3-alpine AS builder

ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev

WORKDIR /src

# Dependencies first so the module cache survives source-only changes.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
    -o /email-triage-bot ./cmd/email-triage-bot

# -----------------------------------------------------------
# Static, non-root, no shell and no package manager. The bot needs only the
# CA bundle distroless ships with; rules and accounts are mounted at /config.
FROM gcr.io/distroless/static-debian12:nonroot

LABEL org.opencontainers.image.source="https://github.com/hedche/email-triage-bot" \
      org.opencontainers.image.licenses="MIT" \
      org.opencontainers.image.description="Scheduled email triage bot: deterministic rules first, AI judgement for the remainder"

COPY --from=builder /email-triage-bot /email-triage-bot

USER nonroot:nonroot
ENTRYPOINT ["/email-triage-bot"]
CMD ["run"]
