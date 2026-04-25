# syntax=docker/dockerfile:1.6

FROM --platform=$BUILDPLATFORM golang:1.25 AS builder

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

WORKDIR /
COPY --from=builder /workspace/manager /manager

USER 65532:65532
EXPOSE 8080 8081
ENTRYPOINT ["/manager"]
