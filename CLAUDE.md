# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Overview

`seehuhn.de/go/websocket` is a server-side WebSocket library (RFC 6455). It provides an `http.Handler` that upgrades HTTP requests to WebSocket connections and hands each connection to a user-supplied `Handle(conn *Conn)` callback. It only implements the server role — there is no client. Requires Go 1.25.

## Commands

```sh
go build ./...
go test ./...                       # unit tests (use goleak to catch goroutine leaks)
go test -run TestBroadcast          # single test
go test -race ./...                 # race detector — important given the concurrency model
go test -fuzz=FuzzReader            # fuzz the frame reader (checks the reader never hangs)
go vet ./...
```

Autobahn TestSuite conformance testing (requires Docker) lives in `testing/autobahn/`:

```sh
cd testing/autobahn && go run . -test 1.*,2.*    # runs a wstest client in Docker against a local echo server; report written to scratch/index.html
```

## Architecture

The whole design centers on passing two singleton objects — one `*sender` and one `*receiver` per connection — between goroutines over buffered channels of capacity 1. **Holding the object off its channel is the lock**; there is no mutex. Understanding this is the key to the codebase.

- **`Conn.senderStore` (`chan *sender`, cap 1)** — a semaphore guarding write access. Any code wanting to write pulls the `sender` out, uses it, and puts it back (`SendText`, `SendBinary`, `SendMessage`/`frameWriter.Close`, `doBroadcast`, and the pong path all follow this). Receiving `nil` (or a closed channel) means the connection is shutting down — no more writes are allowed. `close(senderStore)` is the one-way signal that the close frame has been sent.

- **`Conn.toUser` / `Conn.fromUser` (`chan *receiver`)** — a semaphore guarding read access, ping-ponged between the `readManager` goroutine and the user. `readManager` (started in `conn.initialize`, one per connection) owns the receiver while idle, blocking on the connection; when a data frame arrives it hands the receiver to the user via `toUser`. The user reads the message, then must return the receiver via `fromUser` (done automatically by `autoCloseReader` / the `doReceive*` helpers). **A message must always be read to completion or the connection deadlocks** — this contract is repeated throughout the public API docs.

- **Shutdown** is driven by `readManager` in `reader.go`. Its loop exits on close frame, read error, or protocol failure; it then sends a close frame if one wasn't already sent, closes the TCP connection, records `connInfo`/`clientStatus`/`clientMessage`, and closes `shutdownComplete`. `Conn.Wait()` blocks on `shutdownComplete` and only then may read those result fields. `Conn.Close()` sends the close frame and starts a 3-second timer before force-closing the TCP connection.

- **`shutdownStarted`** (closed by `sender.isShuttingDown` checks and `receiver.failConnection`) is the flag telling writers to stop once the reader has decided to fail the connection.

### Files

- `conn.go` — `Conn` struct, `initialize`, `Close`, `Wait`, status codes (`Status`), `ConnInfo`, `MessageType`.
- `handler.go` — `Handler`, HTTP upgrade/handshake per RFC 6455 §4.2, origin/access checks, subprotocol negotiation, token parsing.
- `reader.go` — `receiver`, `readManager` (the connection lifecycle), frame parsing/unmasking, control-frame handling (ping/pong/close), and all `Receive*`/`Select*` APIs.
- `writer.go` — `sender`, `frameWriter`, frame serialization, `Send*` and `Broadcast*` APIs.
- `error.go` — sentinel errors.

### Multi-connection APIs

`ReceiveOneMessage`, `SelectText`, `SelectBinary` (reader.go) and `BroadcastText`/`BroadcastBinary` (writer.go) operate over a slice of `*Conn` using `reflect.Select` over their per-connection channels. Because `reflect.Select` supports at most 65536 cases and one is reserved for the `context`, these functions **panic if given more than 65535 clients**.

## Conventions

- Every source file starts with the GPL-3.0 header (see any existing `.go` file); new files must include it.
- All wire-format handling must stay RFC 6455 compliant — validate against the Autobahn suite when touching `reader.go`/`writer.go`.
- Recent history shows non-termination/deadlock bugs are the main hazard; when changing the channel choreography, run `go test -race` and the fuzzer, and think in terms of "who holds the sender/receiver right now."
