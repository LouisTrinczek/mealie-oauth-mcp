FROM --platform=$BUILDPLATFORM golang:1.27.3@sha256:2920bb124f1a2e12a1d39f0cab756ae0068441ee1da7655dec7f5e374944095b AS build
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
COPY cmd ./cmd
COPY internal ./internal
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w -X main.version=$VERSION" -o /out/mealie-oauth-mcp ./cmd/mealie-oauth-mcp
# The image has no shell, so /data is created here to be owned by nonroot.
RUN mkdir /out/data

FROM gcr.io/distroless/static-debian13:nonroot@sha256:18f4cc26c9ffa7f20a359298be7deadac5c607c8e03a02b8119f1a5fc2f82c99
COPY --from=build /out/mealie-oauth-mcp /mealie-oauth-mcp
COPY --from=build --chown=nonroot:nonroot /out/data /data
USER nonroot:nonroot
VOLUME /data
EXPOSE 8080
ENTRYPOINT ["/mealie-oauth-mcp"]
