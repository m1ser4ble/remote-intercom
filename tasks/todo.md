# Remote Intercom Reliability Task List

Canonical implementation detail: [`tasks/plan.md`](plan.md)

## Task 1: Restore ephemeral channel expiry and pending permissions

- [ ] RED: final-member expiry deletes channel
- [ ] GREEN: restore registry deletion
- [ ] RED: channel deletion closes pending sockets
- [ ] RED: pending list is unauthorized while status remains allowed
- [ ] GREEN: enforce hub permission and cleanup behavior
- [ ] Verify channel and hub suites
- [ ] Commit

## Task 2: Add relay acknowledgements

- [ ] RED: direct messages require `message.ack`
- [ ] RED: join decisions require `join.decision.ack`
- [ ] RED: five approximately 12 KiB messages require five receives and five ACKs
- [ ] GREEN: emit ACKs after successful operation
- [ ] Preserve correlated errors and distinguish unreachable target
- [ ] Update strict end-to-end event ordering
- [ ] Verify full relay suite
- [ ] Commit

## Task 3: Await message ACKs and reject oversized frames

- [ ] RED: complete serialized frame above 65,536 bytes is rejected before write
- [ ] RED: message promise waits for correlated ACK
- [ ] RED: correlated error and ACK timeout reject without replay
- [ ] GREEN: add protocol types, frame validation, and response waiters
- [ ] Verify client suite
- [ ] Commit

## Task 4: Make tool results acknowledgement-aware

- [ ] RED: pending state remains until decision ACK
- [ ] RED: decision failure/timeout retains pending state
- [ ] GREEN: await message and decision operations
- [ ] Return request and ACK events in tool success
- [ ] Verify tool and extension suites
- [ ] Commit

## Task 5: Detect idle half-open connections

- [ ] RED: open but non-responsive socket times out heartbeat
- [ ] RED: pending connection also reconnects
- [ ] RED: concurrent recovery opens one reconnect
- [ ] RED: explicit disconnect prevents recovery
- [ ] GREEN: add activity tracking and one heartbeat timer
- [ ] GREEN: add one reconnect promise and generation cancellation
- [ ] GREEN: preflight stale network actions
- [ ] Verify extension suite and bundle
- [ ] Commit

## Task 6: Verify complete contract

- [ ] Run the five-message 12 KiB receive-and-ACK regression added in Task 2
- [ ] Run full Go suite
- [ ] Run full TypeScript suite
- [ ] Build extension bundle
- [ ] Check docs, protocol, and constants for consistency
- [ ] Run Ponytail scope audit
- [ ] Commit only if final files changed

## Final acceptance

- [ ] Issue #1 spec satisfied
- [ ] Issue #2 spec satisfied
- [ ] Issue #3 spec satisfied
- [ ] No new dependency, datastore, chunking, or automatic replay
- [ ] Working tree clean
