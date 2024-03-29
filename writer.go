// seehuhn.de/go/websocket - an http server to establish websocket connections
// Copyright (C) 2019  Jochen Voss <voss@seehuhn.de>
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
	"context"
	"io"
	"reflect"
)

const maxHeaderSize = 10

type sender struct {
	w      *bufio.Writer
	header [maxHeaderSize]byte

	// ShutdownStarted is closed when we have started to shut down the connection.
	shutdownStarted <-chan struct{}
}

func (wb *sender) isShuttingDown() bool {
	select {
	case <-wb.shutdownStarted:
		return true
	default:
		return false
	}
}

func (wb *sender) sendFrame(opcode MessageType, body []byte, final bool) error {
	header := wb.header[:]
	header[0] = byte(opcode)
	if final {
		header[0] |= 128
	}

	l := len(body)
	var n int
	switch {
	case l < 126:
		header[1] = byte(l)
		n = 2
	case l < (1 << 16):
		header[1] = 126
		header[2] = byte(l >> 8)
		header[3] = byte(l)
		n = 4
	default:
		header[1] = 127
		header[2] = byte(l >> 56)
		header[3] = byte(l >> 48)
		header[4] = byte(l >> 40)
		header[5] = byte(l >> 32)
		header[6] = byte(l >> 24)
		header[7] = byte(l >> 16)
		header[8] = byte(l >> 8)
		header[9] = byte(l)
		n = 10
	}

	_, err := wb.w.Write(header[:n])
	if err != nil {
		return err
	}
	_, err = wb.w.Write(body)
	if err != nil {
		return err
	}
	if final {
		return wb.w.Flush()
	}
	return nil
}

func (wb *sender) sendCloseFrame(status Status, body []byte) error {
	var buf []byte
	if status != StatusNotSent {
		buf = make([]byte, 2+len(body))
		buf[0] = byte(status >> 8)
		buf[1] = byte(status)
		copy(buf[2:], body)
	}
	return wb.sendFrame(closeFrame, buf, true)
}

type frameWriter struct {
	*sender
	store chan<- *sender
	tp    MessageType
}

func (w *frameWriter) Write(p []byte) (int, error) {
	if w.isShuttingDown() {
		return 0, ErrConnClosed
	}

	err := w.sendFrame(w.tp, p, false)
	if err != nil {
		return 0, err
	}
	w.tp = contFrame
	return len(p), nil
}

func (w *frameWriter) Close() error {
	var err error

	if !w.isShuttingDown() {
		// send the final frame
		err = w.sendFrame(w.tp, nil, true)
	}

	wb := w.sender
	w.sender = nil
	w.store <- wb
	return err
}

// SendMessage starts a new message and returns an io.WriteCloser
// which can be used to send the message body.  The argument tp gives
// the message type (Text or Binary).  Text messages must be sent in
// utf-8 encoded form.
func (conn *Conn) SendMessage(tp MessageType) (io.WriteCloser, error) {
	wb := <-conn.senderStore
	if wb == nil {
		return nil, ErrConnClosed
	}
	// The sender is returned to the conn.senderStore in the
	// frameWriter.Close() method.

	w := &frameWriter{
		sender: wb,
		store:  conn.senderStore,
		tp:     tp,
	}
	return w, nil
}

// SendBinary sends a binary message to the client.
//
// For streaming large messages, use SendMessage() instead.
func (conn *Conn) SendBinary(msg []byte) error {
	wb := <-conn.senderStore
	if wb == nil {
		return ErrConnClosed
	}

	var err error
	if !wb.isShuttingDown() {
		err = wb.sendFrame(Binary, msg, true)
	} else {
		err = ErrConnClosed
	}

	conn.senderStore <- wb
	return err
}

// SendText sends a text message to the client.
func (conn *Conn) SendText(msg string) error {
	wb := <-conn.senderStore
	if wb == nil {
		return ErrConnClosed
	}

	var err error
	if !wb.isShuttingDown() {
		err = wb.sendFrame(Text, []byte(msg), true)
	} else {
		err = ErrConnClosed
	}

	conn.senderStore <- wb
	return err
}

// BroadcastBinary sends a binary message to all clients in the
// given slice.  The return value contains all errors that occurred
// during sending.  The keys of the map are the indices of the
// clients in the slice.
func BroadcastBinary(ctx context.Context, clients []*Conn, msg []byte) map[int]error {
	return doBroadcast(ctx, clients, Binary, msg)
}

// BroadcastBinary sends a text message to all clients in the
// given slice.  The return value contains all errors that occurred
// during sending.  The keys of the map are the indices of the
// clients in the slice.
func BroadcastText(ctx context.Context, clients []*Conn, msg string) map[int]error {
	return doBroadcast(ctx, clients, Text, []byte(msg))
}

func doBroadcast(ctx context.Context, clients []*Conn, tp MessageType, msg []byte) map[int]error {
	numClients := len(clients)
	if numClients > 65535 {
		// select supports at most 65536 cases, and we need one for the context
		panic("too many clients")
	}

	// set up channels for the select statement
	cases := make([]reflect.SelectCase, numClients+1)
	for i, conn := range clients {
		cases[i] = reflect.SelectCase{
			Dir:  reflect.SelectRecv,
			Chan: reflect.ValueOf(conn.senderStore),
		}
	}
	cases[numClients] = reflect.SelectCase{
		Dir:  reflect.SelectRecv,
		Chan: reflect.ValueOf(ctx.Done()),
	}

	disabled := reflect.Zero(reflect.ChanOf(reflect.BothDir,
		reflect.TypeOf(&sender{})))
	todo := numClients
	errors := make(map[int]error)
mainLoop:
	for todo > 0 {
		idx, recv, recvOK := reflect.Select(cases)

		if idx == numClients { // the context was cancelled
			err := ctx.Err()
			for i := 0; i < numClients; i++ {
				if cases[i].Chan != disabled {
					errors[i] = err
				}
			}
			break mainLoop
		}

		cases[idx].Chan = disabled

		if !recvOK { // the connection was closed
			errors[idx] = ErrConnClosed
			todo--
			continue mainLoop
		}

		wb := recv.Interface().(*sender)
		err := wb.sendFrame(tp, msg, true)
		clients[idx].senderStore <- wb
		if err != nil {
			errors[idx] = err
			continue mainLoop
		}
	}
	return errors
}
