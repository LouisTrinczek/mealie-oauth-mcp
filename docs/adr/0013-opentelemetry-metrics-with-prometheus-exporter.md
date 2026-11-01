# 13. Instrument metrics with OpenTelemetry, export them for Prometheus

Status: Accepted

Date: 2026-11-01

## Context

Tracing already uses the OpenTelemetry API, while metrics used the Prometheus
client library directly. That meant two instrumentation APIs in one small
server. Most self-hosters still scrape metrics with Prometheus, so the
`/metrics` endpoint from [ADR 11](0011-observability-and-health-endpoints.md)
has to stay.

## Decision

- Metrics are recorded with the OpenTelemetry metrics API only. Instrument
  definitions live in `internal/metrics`.
- The OpenTelemetry Prometheus exporter feeds a private Prometheus registry,
  which `/metrics` serves on `METRICS_ADDR` as before.
- Scope and target info are turned off, so metric names and labels match the
  earlier Prometheus series.
- Go runtime and process metrics still come from the Prometheus collectors on
  the same registry. The OpenTelemetry runtime instrumentation would add a
  dependency and has no process metrics.
- Metrics are not pushed over OTLP.

## Consequences

- New metrics are written once, against the OpenTelemetry API.
- Existing dashboards and alerts keep working: series names and labels did
  not change.
- `github.com/prometheus/client_golang` remains a dependency, because the
  exporter builds on it.
- Adding an OTLP metrics push later only needs another reader on the meter
  provider.
