# 5. A single instance with SQLite

Status: Accepted

Date: 2026-10-31

## Context

The server targets self-hosters who run Mealie at home or for a household. The
state is small: clients, sessions, hashed tokens and consents. Running
PostgreSQL or several replicas would add setup and failure modes that this
audience does not need.

## Decision

State lives in one SQLite file in `DATA_DIR`, accessed through a pure-Go
driver so the binary stays static. The database uses WAL mode with a busy
timeout. A file lock in the data directory prevents two processes from sharing
it. Migrations are embedded and applied with goose at startup, and the server
refuses to run on a schema newer than it knows.

The `backup` subcommand makes a consistent copy while the server runs. For
continuous protection, the README documents Litestream.

## Consequences

- Deployment is one container and one volume.
- There is no high availability. A restart or an upgrade means a short outage.
- Horizontal scaling is out of scope. If it is ever needed, it needs a new
  storage layer and a new decision record.
- Backups and restore are simple file operations. See the README sections on
  backups.
