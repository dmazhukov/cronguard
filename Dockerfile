# syntax=docker/dockerfile:1.6

FROM --platform=$BUILDPLATFORM golang:1.26 AS builder

ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_DATE=unknown

WORKDIR /workspace

COPY go.mod go.sum ./
RUN go mod download

COPY cmd/ cmd/
COPY api/ api/
COPY internal/ internal/

ENV CGO_ENABLED=0
RUN GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build \
    -trimpath \
    -ldflags "-s -w \
      -X main.version=${VERSION} \
      -X main.commit=${COMMIT} \
      -X main.buildDate=${BUILD_DATE}" \
    -o manager cmd/main.go

FROM gcr.io/distroless/static:nonroot

# OCI image annotations (https://specs.opencontainers.org/image-spec/annotations/).
# `org.opencontainers.image.source` is what GHCR reads to link the image to
# its source repository in the registry UI; supply-chain tools (e.g. Sigstore
# rekor lookups, dependency-track) consume it to trace provenance. The other
# labels are conventional image metadata. Values for version/revision/created
# come from --build-arg in CI; they remain literal strings rather than
# evaluating in the runtime stage.
ARG VERSION=dev
ARG COMMIT=unknown
ARG BUILD_DATE=unknown
LABEL org.opencontainers.image.source="https://github.com/dmazhukov/cronguard"
LABEL org.opencontainers.image.description="SLO-style observability operator for Kubernetes CronJobs"
LABEL org.opencontainers.image.licenses="Apache-2.0"
LABEL org.opencontainers.image.title="CronGuard"
LABEL org.opencontainers.image.url="https://github.com/dmazhukov/cronguard"
LABEL org.opencontainers.image.documentation="https://github.com/dmazhukov/cronguard#readme"
LABEL org.opencontainers.image.vendor="Dmitrii Zhukov"
LABEL org.opencontainers.image.version="${VERSION}"
LABEL org.opencontainers.image.revision="${COMMIT}"
LABEL org.opencontainers.image.created="${BUILD_DATE}"

WORKDIR /
COPY --from=builder /workspace/manager /manager

USER 65532:65532
EXPOSE 8080 8081
ENTRYPOINT ["/manager"]
