# syntax=docker/dockerfile:1

FROM --platform=$BUILDPLATFORM golang:1.26-bookworm AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY cmd/ ./cmd/
COPY internal/ ./internal/
ARG TARGETOS
ARG TARGETARCH
ARG TARGETPLATFORM
ARG BUILDPLATFORM
RUN CGO_ENABLED=0 GOOS="$TARGETOS" GOARCH="$TARGETARCH" \
    go build -trimpath -ldflags="-s -w" -o /out/bps-local ./cmd/bps-local \
    && if [ "$TARGETPLATFORM" = "$BUILDPLATFORM" ]; then /out/bps-local --version; fi

FROM gcr.io/distroless/static-debian13:nonroot
COPY --from=build /out/bps-local /usr/local/bin/bps-local
COPY LICENSE /usr/share/doc/bps-local/LICENSE
ENTRYPOINT ["/usr/local/bin/bps-local"]
