#!/bin/sh
set -eu

work=$(mktemp -d)
project=mealie-proxies-$(basename "$work" | tr -cd 'a-zA-Z0-9' | tr '[:upper:]' '[:lower:]')
export PROXY_TEST_TLS_DIR="$work/tls"
compose() {
    docker compose -p "$project" -f test/integration/compose.yml -f test/integration/proxies/compose.yml "$@"
}
cleanup() {
    compose down -v
    rm -rf "$work"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

mkdir -m 755 "$PROXY_TEST_TLS_DIR"
make_ca() {
    openssl req -x509 -newkey rsa:2048 -nodes -days 1 -subj "/CN=$1" \
        -keyout "$work/$1.key" -out "$PROXY_TEST_TLS_DIR/$1.pem" 2>"$work/openssl.log"
}
issue() {
    openssl req -new -newkey rsa:2048 -nodes -subj "/CN=$1" \
        -keyout "$PROXY_TEST_TLS_DIR/$1.key" -out "$work/$1.csr" 2>"$work/openssl.log"
    printf 'subjectAltName=%s\nextendedKeyUsage=serverAuth\n' "$2" > "$work/$1.ext"
    openssl x509 -req -days 1 -in "$work/$1.csr" -CA "$PROXY_TEST_TLS_DIR/ca.pem" \
        -CAkey "$work/ca.key" -CAserial "$work/ca.srl" -CAcreateserial -extfile "$work/$1.ext" \
        -out "$PROXY_TEST_TLS_DIR/$1.pem" 2>"$work/openssl.log"
}
make_ca ca
make_ca unrelated
issue edge DNS:traefik,DNS:nginx,DNS:caddy,DNS:haproxy
issue mcp DNS:mcp.test
issue mealie IP:127.0.0.1
cat "$PROXY_TEST_TLS_DIR/edge.pem" "$PROXY_TEST_TLS_DIR/edge.key" > "$PROXY_TEST_TLS_DIR/edge-bundle.pem"
chmod 644 "$PROXY_TEST_TLS_DIR/"*.key "$PROXY_TEST_TLS_DIR/edge-bundle.pem"

compose up --attach it --abort-on-container-exit --exit-code-from it
for proxy in traefik nginx caddy haproxy; do
    compose logs --no-color "$proxy" > "$work/proxy.log"
    if ! grep -qF '/login/password' "$work/proxy.log"; then
        printf '%s\n' "$proxy did not log any password route traffic" >&2
        exit 1
    fi
    if grep -qF 'MyPassword' "$work/proxy.log"; then
        printf '%s\n' "$proxy logged the disposable Mealie password" >&2
        exit 1
    fi
done
