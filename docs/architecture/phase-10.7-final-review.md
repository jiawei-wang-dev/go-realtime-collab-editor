# Phase 10.7 Final Architecture and Code Review

Status: **PASS**
Date: 2026-11-09

## Review scope

This review covers the browser client introduced in Phase 10, the WebSocket adapter, collaborative cursor and selection presence, and their integration with the existing distributed backend. It also rechecks the security boundaries that must remain authoritative: authenticated identity, Room serialization, etcd Ownership, MySQL Fencing Token validation, Redis session revocation, and client-side pending-edit recovery.

## Architecture findings

- **One collaboration core:** QUIC clients and browser WebSocket clients enter the same transport-neutral Room Manager. The WebSocket gateway adapts frames and connection lifecycle; it does not duplicate OT, permission, Ownership, or persistence logic.
- **Single writer per document:** each active document Room remains a single goroutine and the only in-memory writer for content, Version, history, participants, and ephemeral cursor state.
- **Distributed write safety:** etcd Lease ownership determines the active Room, while MySQL transactions validate `WriteAuthority` and Fencing Token for durable edits, permission changes, and snapshots. Owner Redirect never permits a non-owner node to write locally as a fallback.
- **Authenticated browser boundary:** browser identity is derived from validated JWT and Redis session state. Refresh credentials remain in an HttpOnly cookie. Cursor payloads cannot select their own user identity or display name.
- **Durability separation:** edits increment Version and are committed with Operation Log entries; cursor and selection traffic remains ephemeral and never changes Version, MySQL content, Operation Log, or snapshots.
- **Recovery:** the browser preserves pending edits in IndexedDB and reuses the original `request_id`; the native client retains its SQLite recovery path. Reconnect, SYNC, RESYNC_REQUIRED, Owner Redirect, and fencing behavior remain intact.

## Cursor and selection review

Cursor offsets use Unicode code points, matching the OT operation coordinate system. The browser converts Monaco's UTF-16 offsets at the protocol boundary. The Owner Room validates read permission, Version, bounds, and per-client sequence before broadcasting a server-derived identity. Stale positions are transformed through retained history, and accepted edits transform stored remote positions.

The browser throttles cursor updates, sends a heartbeat, republishes after reconnect, and disposes Monaco decorations on document changes, navigation, logout, and component unmount. A local expiry removes abandoned decorations. Invalid ephemeral updates that cross an edit acknowledgement are discarded without weakening validation of edits or terminating an otherwise healthy document session.

## Readability and maintainability

The new code keeps protocol conversion, connection orchestration, pending-edit handling, and Monaco decoration rendering in separate modules. Backend cursor handling follows the existing Room event model instead of introducing another shared-state manager. No high-risk refactor was applied to the established OT, Ownership, Fencing, Session, Snapshot, or Replay implementations during this phase.

## Security and dependency review

- No tracked private key marker, certificate, local database, Terraform state, runtime log, or real `.env` file was found in the proposed commit set.
- `go mod verify`, `govulncheck`, and `npm audit --audit-level=high` completed successfully. `govulncheck` reported no called vulnerable symbols; it noted vulnerabilities only in unreachable dependency code.
- Dependency-license inventory found standard permissive, Apache, MPL, and content-data licenses in the frontend tree. The package root itself has no declared npm license; this matches the repository's existing licensing state and was not changed in Phase 10.
- Docker Compose configuration renders with an explicit project name. Terraform 1.17.1 `fmt -check`, `init`, and `validate` pass in an isolated Linux container copy without changing the repository lock file.

## Acceptance evidence

- Real two-browser editing and recovery: PASS.
- Real two-node Owner Redirect, cursor presence, fencing failover, and operation-log invariants: PASS.
- Cross-engine pairings covering Chromium, Firefox, and WebKit: PASS.
- Go tests, race detector, vet, and build: PASS.
- Frontend lint, typecheck, unit tests, production build, and E2E suites: PASS.

Detailed evidence is recorded in the Phase 10.4, 10.5, 10.6 reports and the [Phase 10.7 quality gate](../operations/phase-10.7-final-quality-gate.md).

## Remaining boundaries

- Native Safari 26.5 is installed but remains **NOT VERIFIED** because Safari's user-controlled **Allow remote automation** setting is disabled. Playwright WebKit passed and is not presented as native Safari.
- Cursor presence is intentionally not persisted or replayed; active clients republish after reconnect or Owner failover.
- Browser offline recovery replays queued linear edits sequentially and is not a general multi-operation local rebase engine.
- Clipboard permission prompts and production-scale capacity are outside the automated acceptance scope.
- Google Cloud infrastructure is prepared as Terraform only; no paid cloud resources were created and no deployment was performed.
