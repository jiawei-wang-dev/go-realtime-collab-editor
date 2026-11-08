# Phase 10.5 Cursor and Selection Design

## Scope and invariants

Cursor and selection data is ephemeral Room state. It is authenticated and
ordered through the existing WebSocket connection and authoritative Room event
queue, but it is never treated as an edit: it does not increment document
version, enter the operation log, create a snapshot, or call Storage. QUIC
clients remain protocol-compatible and simply do not opt into cursor presence.

## Protocol

The browser sends `cursor_update` after JOIN/RESUME:

```json
{
  "type": "cursor_update",
  "doc_id": "main.go",
  "cursor": {
    "anchor": 12,
    "head": 20,
    "version": 7,
    "sequence": 31
  }
}
```

`anchor` is the fixed end of a Monaco selection and `head` is its active end,
so selection direction is preserved. A collapsed selection has equal offsets.
The Owner broadcasts `cursor_changed` with the authenticated `user_id` and the
username resolved by the authentication service. `cursor_removed` removes a
departed user's decoration. A newly joined client receives the current cursor
snapshot after its initial document state.

## Server flow

```text
WebSocket AUTH/JOIN
        |
authenticated Principal + Room membership
        |
cursor_update rate/shape check
        |
Room cursorEvent queue
        |
permission, sequence and version validation
        |
transform offsets through retained OT history
        |
update ephemeral cursor map and broadcast
```

The gateway accepts cursor messages only after JOIN/RESUME. Its per-connection
limiter rejects sustained cursor floods without affecting EDIT ordering. The
Room remains the single writer of document, client and cursor state. Owner
Redirect therefore routes both edit and cursor traffic to exactly the same
Owner; Redis is not used as a second cursor authority and no cross-node loop is
possible.

## OT position transformation

An update carries the document version at which Monaco produced its offsets.
For each retained operation after that version, both anchor and head are
transformed independently:

- insert before or at an offset shifts it right by inserted text length;
- delete entirely before an offset shifts it left by deleted length;
- an offset inside a deleted range collapses to the delete start;
- transforming both endpoints independently preserves forward/reverse
  selection direction.

After every committed edit, the Room applies the same position transform to all
stored cursors before broadcasting their new authoritative version. A cursor
older than the retained history, ahead of the document, negative, or outside
the resulting content is rejected. Cursor processing never changes the OT edit
algorithm.

## Ordering, lifecycle and recovery

Each connection uses a strictly increasing `sequence`. The Room ignores a
sequence that is not newer than the last accepted value for that client, so a
late event cannot overwrite newer state. WebSocket message order and the Room
queue provide the remaining ordering boundary.

Leave, session revocation, slow-client removal, Room shutdown, and loss of read
permission remove cursor state. On reconnect the browser completes
AUTH/RESUME/SYNC first and then publishes its current Monaco selection using a
new sequence. Browser cursor heartbeats refresh otherwise stationary state;
remote decorations expire locally if updates stop without a clean Leave.

## Frontend lifecycle

Monaco's selection event is converted to model offsets and throttled outside
React state. Remote states are stored in a ref-backed map and rendered with a
single `IEditorDecorationsCollection`, so cursor movement does not rerender the
editor or mutate its text. The collection and Monaco listeners are disposed on
document change/unmount. Stable user-ID hashing selects one of eight accessible
colors; current-user events are ignored.

## Limits and validation

- client throttle: at most one immediate update per 50 ms, with the latest
  pending selection sent after the interval;
- server allowance: short bursts up to 30 cursor messages per second per
  connection, after which updates are rejected;
- heartbeat: 10 seconds; local remote-state expiry: 30 seconds;
- offsets are non-negative integers and must resolve inside current content;
- sequence and version are unsigned protocol values;
- the existing 1 MiB WebSocket read limit still bounds the complete message;
- username is server-derived and cannot be supplied by the browser.

These choices keep cursor latency independent from persistence while retaining
the Room, Ownership, authentication, permission and slow-client safety
boundaries already used by document edits.
