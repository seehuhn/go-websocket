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
	"fmt"
	"net"
	"net/url"
	"sync"
	"time"
)

// Conn represents a websocket connection initiated by a client.  All fields
// are read-only.  Use a Handler to obtain Conn objects.
//
// It is ok to access a Conn from different goroutines concurrently.  The
// connection must be closed using the Close() method after use, to free all
// allocated resources.
type Conn struct {
	ResourceName string
	Origin       *url.URL
	RemoteAddr   string
	Protocol     string
	RequestData  interface{} // as returned by Handler.AccessAllowed()

	raw net.Conn
	rw  *bufio.ReadWriter

	// ReaderDone is closed when the reader goroutine has finished.
	// After this point, the reader will not access the Conn object
	// any more and will not send any more control messages.
	readerDone <-chan struct{}

	toUser   <-chan *readBompel
	fromUser chan<- *readBompel

	writerDone <-chan struct{}

	getFrameWriter   <-chan *frameWriter
	sendControlFrame chan<- *frame

	closeMutex    sync.Mutex
	isClosed      bool // no more messages can be sent
	clientStatus  Status
	clientMessage string
}

// MessageType encodes the type of a websocket message.
type MessageType byte

// Websocket message types as define in RFC 6455.
// See: https://tools.ietf.org/html/rfc6455#section-5.6
const (
	Text   MessageType = 1
	Binary MessageType = 2

	contFrame  MessageType = 0
	closeFrame MessageType = 8
	pingFrame  MessageType = 9
	pongFrame  MessageType = 10
)

func (tp MessageType) String() string {
	switch tp {
	case Text:
		return "text"
	case Binary:
		return "binary"
	case contFrame:
		return "continuation"
	case closeFrame:
		return "close"
	case pingFrame:
		return "ping"
	case pongFrame:
		return "pong"
	default:
		return fmt.Sprintf("MessageType(%d)", tp)
	}
}

// Status describes the reason for the closure of a websocket
// connection.
type Status uint16

// Websocket status codes as defined in RFC 6455, for use in the
// Conn.Close() method.
// See: https://tools.ietf.org/html/rfc6455#section-7.4.1
const (
	StatusOK                     Status = 1000
	StatusGoingAway              Status = 1001
	StatusProtocolError          Status = 1002
	StatusUnsupportedType        Status = 1003
	StatusNotSent                Status = 1005 // never sent over the wire
	StatusDropped                Status = 1006 // never sent over the wire
	StatusInvalidData            Status = 1007
	StatusPolicyViolation        Status = 1008
	StatusTooLarge               Status = 1009
	StatusClientMissingExtension Status = 1010 // only sent by client
	StatusInternalServerError    Status = 1011
)

func (code Status) clientCanSend() bool {
	if code >= 3000 && code < 5000 || code == StatusClientMissingExtension {
		return true
	}
	return knownValidCode[code]
}

func (code Status) serverCanSend() bool {
	if code >= 3000 && code < 5000 {
		return true
	}
	return knownValidCode[code]
}

var knownValidCode = map[Status]bool{
	StatusOK:              true,
	StatusGoingAway:       true,
	StatusProtocolError:   true,
	StatusUnsupportedType: true,
	// StatusNotSent is never sent over the wire
	// StatusDropped is never sent over the wire
	StatusInvalidData:     true,
	StatusPolicyViolation: true,
	StatusTooLarge:        true,
	// StatusClientMissingExtension is only sent by the client
	StatusInternalServerError: true,
}

// GetStatus returns the status and message the client sent when
// closing the connection.  This function blocks until the connection
// is closed.  The returned status has the following meaning:
//
//   - StatusDropped indicates that the client dropped the connection
//     without sending a close frame.
//   - StatusNotSent indicates that the client sent a close frame,
//     but did not include a valid status code.
//   - Any other value is the status code sent by the client.
func (conn *Conn) GetStatus() (Status, string) {
	<-conn.readerDone

	conn.closeMutex.Lock()
	defer conn.closeMutex.Unlock()
	status := conn.clientStatus
	if status == 0 {
		// no close frame has been received
		status = StatusDropped
	}
	return status, conn.clientMessage
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

	err := conn.sendCloseFrame(code, body)
	if err != nil {
		return err
	}

	// Give the client 3 seconds to close the connection, before closing it
	// from our end.
	needsClose := true
	timeOut := time.NewTimer(3 * time.Second)
	select {
	case <-conn.readerDone:
		if !timeOut.Stop() {
			<-timeOut.C
		}
	case <-timeOut.C:
		conn.raw.Close() // force-stop the reader
		needsClose = false
	}

	// The reader uses conn.sendControlFrame, so we must be sure the
	// reader has stopped before we can stop the writer by closing
	// [conn.sendControlFrame].
	<-conn.readerDone

	conn.closeMutex.Lock()
	if !conn.isClosed {
		close(conn.sendControlFrame)
		conn.isClosed = true
	}
	conn.closeMutex.Unlock()

	if needsClose {
		<-conn.writerDone
		conn.raw.Close()
	}

	return nil
}

func (conn *Conn) sendCloseFrame(status Status, body []byte) error {
	var buf []byte
	if status != StatusNotSent {
		buf = make([]byte, 2+len(body))
		buf[0] = byte(status >> 8)
		buf[1] = byte(status)
		copy(buf[2:], body)
	}
	ctl := framePool.Get().(*frame)
	ctl.Opcode = closeFrame
	ctl.Body = buf
	ctl.Final = true

	conn.closeMutex.Lock()
	defer conn.closeMutex.Unlock()
	if conn.isClosed {
		return ErrConnClosed
	}
	conn.sendControlFrame <- ctl
	return nil
}

type header struct {
	Length int64
	Mask   [4]byte
	Final  bool
	Opcode MessageType
}

type frame struct {
	Body   []byte
	Final  bool
	Opcode MessageType
}

var framePool = sync.Pool{
	New: func() interface{} {
		return new(frame)
	},
}
