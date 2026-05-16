# syntax=docker/dockerfile:1.7

FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .

ARG TARGETOS
ARG TARGETARCH
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w" -o /out/tgrss .

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/tgrss /tgrss

ENV ADDR=:8080 \
    INTERVAL=5m \
    MAX_MESSAGES=100 \
    BASE_PATH=

EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/tgrss"]
