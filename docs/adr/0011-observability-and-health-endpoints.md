# 11. Observability: separate metrics listener, optional tracing, split health checks

Status: Accepted

Date: 2026-10-31

## Context

Operators need metrics, traces and health checks. The server also sits behind
a public URL, so anything it exposes there is reachable by anyone. Metrics and
traces can leak routes, users or tokens if they are careless. A single health
endpoint cannot serve both restart decisions and traffic routing: a Mealie
outage should stop traffic, not restart the process.

## Decision

- Prometheus metrics are served on a separate listener, `METRICS_ADDR`
  (default `127.0.0.1:9090`). Setting it empty disables them. `/metrics` on the
  public listener returns `404`.
- Tracing is optional. It turns on when `OTEL_EXPORTER_OTLP_ENDPOINT` is set
  and uses OTLP over HTTP. Spans carry route patterns and tool names, never
  URLs, queries, headers, tokens or client addresses.
- `/healthz` is liveness only and always answers while the process runs.
  `/readyz` also checks the database and Mealie.
- Audit events are JSON log lines with `log_type` set to `audit`.

## Consequences

- Metrics are not reachable from the internet unless an operator exposes them.
- Operators in Docker must set `METRICS_ADDR` to scrape across containers.
- A Mealie outage makes `/readyz` fail but leaves `/healthz` green, so
  orchestrators stop routing without restarting.
- Labels use bounded values to keep metric cardinality low.
