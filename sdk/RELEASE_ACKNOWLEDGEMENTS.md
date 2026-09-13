# Release loader session contract

Go, TypeScript, synchronous Python, and asynchronous Python release loaders require
server support for release sessions and instance targets. Registration failure is
an error with upgrade guidance; loaders do not fall back to legacy release watches.
Ordinary parameter and secret subscriptions do not require release sessions.

Each call to run a loader represents a new execution and owns a unique session ID,
with a fresh sequence and retained event set. Network reconnects within that run
preserve its session, sequence, and retained events. Running the same loader again
prepares its configuration again and therefore creates a new session, even when
reusing a stable instance name. Concurrent runs of one loader are invalid.

Each new lifecycle event receives an increasing, nonzero session sequence. Replay
sends retained events in sequence order and preserves the entire original payload,
including its sequence and timestamp. Transport send success does not establish
server acceptance; reconnects replay retained events. The server reduces events
using assigned target revision and sequence, not arrival time or lifecycle rank.
The SDK retains the latest event for each lifecycle state, so sequences may contain
gaps; contiguous delivery is not required.

Target revisions and fleet activation revisions are separate identities. Rollback
or pin changes may select a lower release version with a newer target revision.
An unchanged successfully applied target does not generate new lifecycle events
on reconciliation. A genuine failed attempt can retry the same target with new
sequences and eventually report applied. Replayed received/prepared events cannot
undo a newer applied event.

`testdata/release_ack_conformance.json` is consumed by the SDK test suites. It
specifies lifecycle attempts and the retained sequences after state coalescing;
tests verify causal replay order and payload identity across reconnects, including
rejected attempts followed by successful retries.
