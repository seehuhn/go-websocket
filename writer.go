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
	"time"
)

const maxHeaderSize = 10

type writeBompel struct {
	w      *bufio.Writer
	header [maxHeaderSize]byte
}

func (wb *writeBompel) sendFrame(opcode MessageType, body []byte, final bool) error {
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

func (wb *writeBompel) sendCloseFrame(status Status, body []byte) error {
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
	*writeBompel
	store chan<- *writeBompel
	tp    MessageType
}

func (w *frameWriter) Write(p []byte) (int, error) {
	err := w.sendFrame(w.tp, p, false)
	if err != nil {
		// TODO(voss): close the connection
		return 0, err
	}
	w.tp = contFrame
	return len(p), nil
}

func (w *frameWriter) Close() error {
	// send the final frame
	err := w.sendFrame(w.tp, nil, true)
	if err != nil {
		// TODO(voss): close the connection
		return err
	}

	wb := w.writeBompel
	w.writeBompel = nil
	w.store <- wb
	return nil
}

// Close terminates a websocket connection and frees all associated resources.
// The connection cannot be used any more after Close() has been called.
//
// The status code indicates whether the connection completed successfully, or
// due to an error.  Use StatusOK for normal termination, and one of the other
// status codes in case of errors. Use StatusNotSent to not send a status code.
//
// The message can be used to provide additional information to the client for
// debugging.  The utf-8 representation of the string can be at most 123 bytes
// long, otherwise ErrTooLarge is returned.
func (conn *Conn) Close(code Status, message string) error {
	if !(code.serverCanSend() || code == StatusNotSent) {
		return ErrStatusCode
	}

	body := []byte(message)
	if len(body) > 125-2 {
		return ErrTooLarge
	}

	wb := <-conn.writeBompelStore
	if wb != nil {
		close(conn.writeBompelStore) // prevent further writes

		err := wb.sendCloseFrame(code, body)
		if err != nil {
			return err
		}
	}

	// Give the client 3 seconds to close the connection, before closing it
	// from our end.
	go func() {
		timeOut := time.NewTimer(3 * time.Second)
		select {
		case <-conn.readerDone:
			if !timeOut.Stop() {
				<-timeOut.C
			}
		case <-timeOut.C:
			conn.raw.Close() // force-stop the reader
		}
	}()

	return nil
}

// SendMessage starts a new message and returns an io.WriteCloser
// which can be used to send the message body.  The argument tp gives
// the message type (Text or Binary).  Text messages must be sent in
// utf-8 encoded form.
func (conn *Conn) SendMessage(tp MessageType) (io.WriteCloser, error) {
	wb := <-conn.writeBompelStore
	if wb == nil {
		return nil, ErrConnClosed
	}

	w := &frameWriter{
		writeBompel: wb,
		store:       conn.writeBompelStore,
		tp:          tp,
	}
	return w, nil
}

// SendBinary sends a binary message to the client.
//
// For streaming large messages, use SendMessage() instead.
func (conn *Conn) SendBinary(msg []byte) error {
	wb := <-conn.writeBompelStore
	if wb == nil {
		return ErrConnClosed
	}

	err := wb.sendFrame(Binary, msg, true)
	if err != nil {
		return err
	}

	conn.writeBompelStore <- wb

	return nil
}

// SendText sends a text message to the client.
func (conn *Conn) SendText(msg string) error {
	wb := <-conn.writeBompelStore
	if wb == nil {
		return ErrConnClosed
	}

	err := wb.sendFrame(Text, []byte(msg), true)
	if err != nil {
		return err
	}

	conn.writeBompelStore <- wb

	return nil
}

func BroadcastText(ctx context.Context, clients []*Conn, msg string) map[int]error {
	panic("not implemented")
}
