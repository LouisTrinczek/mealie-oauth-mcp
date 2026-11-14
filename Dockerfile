FROM --platform=$BUILDPLATFORM golang:1.27.4@sha256:5e8afd493a7124a8586e134d9b532731161d4b9becf9dee5a5956b107426f93d AS build
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

FROM gcr.io/distroless/static-debian13:nonroot@sha256:25ac8f0c3b2334a9eb05452266bd4697864a8c8ef0ef71e5e6480d7f92868329
COPY --from=build /out/mealie-oauth-mcp /mealie-oauth-mcp
COPY --from=build --chown=nonroot:nonroot /out/data /data
USER nonroot:nonroot
VOLUME /data
EXPOSE 8080
HEALTHCHECK --interval=30s --timeout=5s --start-period=60s --start-interval=2s --retries=3 \
    CMD ["/mealie-oauth-mcp", "healthcheck"]
ENTRYPOINT ["/mealie-oauth-mcp"]
