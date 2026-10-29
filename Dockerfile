FROM --platform=$BUILDPLATFORM golang:1.27.1@sha256:162be5298a40ed317005c8339c6de4d10d3eef336d66dc8e9259b03ab9d3a6d2 AS build
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

FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
COPY --from=build /out/mealie-oauth-mcp /mealie-oauth-mcp
COPY --from=build --chown=nonroot:nonroot /out/data /data
USER nonroot:nonroot
VOLUME /data
EXPOSE 8080
ENTRYPOINT ["/mealie-oauth-mcp"]
