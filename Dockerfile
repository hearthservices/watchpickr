# syntax=docker/dockerfile:1

# Build on the host platform and cross-compile, much faster than emulation.
FROM --platform=$BUILDPLATFORM golang:1.26.6 AS build
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src

# Dependencies first, so code changes reuse this layer.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
ARG VERSION=docker
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath \
        -ldflags "-s -w -X main.version=${VERSION}" \
        -o /out/watchpickr ./cmd/watchpickr

# Created here so a fresh named volume is owned by nonroot. Distroless has no
# shell to chown with.
RUN mkdir -p /out/data

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/watchpickr /watchpickr
COPY --from=build --chown=nonroot:nonroot /out/data /data

# Static binary (CGO off). Distroless already has the CA certs TMDb and OMDb need.
WORKDIR /data
VOLUME /data
ENV WATCHPICKR_ADDR=:8080 \
    WATCHPICKR_DB=/data/watchpickr.db \
    WATCHPICKR_POSTER_DIR=/data/posters
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/watchpickr"]
