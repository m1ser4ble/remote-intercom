# Remote Intercom Reliability Task List

Canonical implementation detail: [`tasks/plan.md`](plan.md)

## Task 1: Restore ephemeral channel expiry and pending permissions

- [x] RED: final-member expiry deletes channel
- [x] GREEN: restore registry deletion
- [x] RED: channel deletion closes pending sockets
- [x] RED: pending list is unauthorized while status remains allowed
- [x] GREEN: enforce hub permission and cleanup behavior
- [x] Verify channel and hub suites
- [x] Commit

## Task 2: Add relay acknowledgements

- [x] RED: direct messages require `message.ack`
- [x] RED: join decisions require `join.decision.ack`
- [x] RED: five approximately 12 KiB messages require five receives and five ACKs
- [x] GREEN: emit ACKs after successful operation
- [x] Preserve correlated errors and distinguish unreachable target
- [x] Update strict end-to-end event ordering
- [x] Verify full relay suite
- [x] Commit

## Task 3: Await message ACKs and reject oversized frames

- [x] RED: complete serialized frame above 65,536 bytes is rejected before write
- [x] RED: message promise waits for correlated ACK
- [x] RED: correlated error and ACK timeout reject without replay
- [x] GREEN: add protocol types, frame validation, and response waiters
- [x] Verify client suite
- [x] Commit atomically with Task 4 to preserve the TypeScript interface boundary

## Task 4: Make tool results acknowledgement-aware

- [x] RED: pending state remains until decision ACK
- [x] RED: decision failure/timeout retains pending state
- [x] GREEN: await message and decision operations
- [x] Return request and ACK events in tool success
- [x] Verify tool and extension suites
- [x] Commit atomically with Task 3

## Task 5: Detect idle half-open connections

- [x] RED: open but non-responsive socket times out heartbeat
- [x] RED: pending connection also reconnects
- [x] RED: concurrent recovery opens one reconnect
- [x] RED: explicit disconnect prevents recovery
- [x] GREEN: add activity tracking and one heartbeat timer
- [x] GREEN: add one reconnect promise and generation cancellation
- [x] GREEN: preflight stale network actions
- [x] Verify extension suite and bundle
- [x] Commit

## Task 6: Verify complete contract

- [x] Run the five-message 12 KiB receive-and-ACK regression added in Task 2
- [x] Run full Go suite
- [x] Run full TypeScript suite
- [x] Build extension bundle
- [x] Check docs, protocol, and constants for consistency
- [x] Run Ponytail scope audit
- [x] Commit final review fixes and checklist

## Final acceptance

- [x] Issue #1 spec satisfied
- [x] Issue #2 spec satisfied
- [x] Issue #3 spec satisfied
- [x] No new dependency, datastore, chunking, or automatic replay
- [x] Working tree clean after final commit
