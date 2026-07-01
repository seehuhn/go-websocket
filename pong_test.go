// seehuhn.de/go/websocket - an http server to establish websocket connections
// Copyright (C) 2026  Jochen Voss <voss@seehuhn.de>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU General Public License for more details.
//
// You should have received a copy of the GNU General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.

package websocket

import (
	"bufio"
	"io"
	"net"
	"runtime"
	"testing"
	"testing/synctest"
)

// TestPingFloodBounded makes sure that a client flooding the connection with
// ping frames while the server holds the sender cannot force the server to
// spawn an unbounded number of goroutines.  At most a single responder
// goroutine is expected, regardless of the number of pings.
//
// The test runs inside a synctest bubble: synctest.Wait blocks until every
// other goroutine is durably blocked, so we can measure the goroutine count at
// the exact moment the reader has drained every ping — no sleeps, no slack in
// the threshold.  The bubble also fails the test if any goroutine is still
// alive at the end, which subsumes the goroutine-leak check.
func TestPingFloodBounded(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		client, server := net.Pipe()
		rw := bufio.NewReadWriter(bufio.NewReader(server), bufio.NewWriter(server))
		conn := &Conn{}
		conn.initialize(server, rw)

		// Drain everything the server sends towards the client so that pong
		// and close frames never block on a full pipe.
		drained := make(chan struct{})
		go func() {
			defer close(drained)
			io.Copy(io.Discard, client)
		}()

		// Hold the sender open (as if we were streaming a message).  This
		// forces every incoming ping onto the deferred pong path.
		w, err := conn.SendMessage(Binary)
		if err != nil {
			t.Fatal(err)
		}

		base := runtime.NumGoroutine()

		// Flood the connection with ping frames.  net.Pipe is synchronous, so
		// the writes ping-pong with the reader; a separate goroutine keeps the
		// main goroutine free to call synctest.Wait afterwards.
		const nPings = 500
		var frame []byte
		frame = appendFrame(frame, pingFrame, nil, true)
		flooded := make(chan struct{})
		go func() {
			defer close(flooded)
			for range nPings {
				if _, err := client.Write(frame); err != nil {
					return
				}
			}
		}()

		synctest.Wait() // every ping has been drained and the reader is idle

		spawned := runtime.NumGoroutine() - base
		if spawned > 2 { // one responder, plus the flooder until it exits
			t.Errorf("ping flood spawned %d goroutines; want O(1)", spawned)
		}

		// Clean up.
		<-flooded
		w.Close()
		conn.Close(StatusOK, "")
		client.Close()
		conn.Wait()
		<-drained
	})
}

// readServerFrame reads a single unmasked frame sent by the server.  It only
// handles the short payloads (< 126 bytes) used in these tests.
func readServerFrame(t *testing.T, r *bufio.Reader) (MessageType, []byte) {
	t.Helper()
	b0, err := r.ReadByte()
	if err != nil {
		t.Fatalf("read frame: %v", err)
	}
	b1, err := r.ReadByte()
	if err != nil {
		t.Fatalf("read frame: %v", err)
	}
	if b1&128 != 0 {
		t.Fatal("server frame is unexpectedly masked")
	}
	payload := make([]byte, int(b1&127))
	if _, err := io.ReadFull(r, payload); err != nil {
		t.Fatalf("read payload: %v", err)
	}
	return MessageType(b0 & 15), payload
}

// TestPongDelivered checks that pings are actually answered: inline when the
// sender is free, and via the responder (with coalescing to the most recent
// ping) when the sender is busy.
func TestPongDelivered(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		client, server := net.Pipe()
		rw := bufio.NewReadWriter(bufio.NewReader(server), bufio.NewWriter(server))
		conn := &Conn{}
		conn.initialize(server, rw)
		cr := bufio.NewReader(client)

		// 1. Fast path: ping while the sender is free -> pong echoes the
		// payload.
		var ping []byte
		ping = appendFrame(ping, pingFrame, []byte("hi"), true)
		go func() { client.Write(ping) }()
		if op, payload := readServerFrame(t, cr); op != pongFrame || string(payload) != "hi" {
			t.Fatalf("fast-path pong: op=%s payload=%q", op, payload)
		}

		// 2. Busy path with coalescing: hold the sender, send three pings, then
		// release.  Per RFC 6455 section 5.5.3 only the most recent ping
		// ("three") must be answered.
		w, err := conn.SendMessage(Binary)
		if err != nil {
			t.Fatal(err)
		}
		var pings []byte
		for _, s := range []string{"one", "two", "three"} {
			pings = appendFrame(pings, pingFrame, []byte(s), true)
		}
		go func() { client.Write(pings) }()
		synctest.Wait() // all three pings consumed; responder blocked on sender

		// Releasing the sender writes an empty final Binary frame and unblocks
		// the pong; both writes block on the pipe, so close from a goroutine
		// while the main goroutine reads.
		go w.Close()

		if op, _ := readServerFrame(t, cr); op != Binary {
			t.Fatalf("expected empty binary frame from writer close, got %s", op)
		}
		// ...then the coalesced pong for the most recent ping.
		if op, payload := readServerFrame(t, cr); op != pongFrame || string(payload) != "three" {
			t.Fatalf("coalesced pong: op=%s payload=%q, want pong \"three\"", op, payload)
		}

		go io.Copy(io.Discard, client)
		conn.Close(StatusOK, "")
		client.Close() // EOF for the reader, so shutdown does not wait for the timer
		conn.Wait()
	})
}
