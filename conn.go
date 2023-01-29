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
	"fmt"
	"net"
	"net/url"
	"sync"
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

	writeBompelStore chan *writeBompel

	// ReaderDone is closed when the reader goroutine has finished.
	// After this point, the reader will not access the Conn object
	// any more and will not send any more control messages.
	readerDone <-chan struct{}
	toUser     <-chan *readBompel
	fromUser   chan<- *readBompel

	closeMutex    sync.Mutex
	closeReason   CloseReason
	clientStatus  Status
	clientMessage string
}

type CloseReason int

const (
	noReason CloseReason = iota
	ServerClosed
	ClientClosed
	ProtocolViolation
	WrongMessageType
	ConnDropped
)

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

type header struct {
	Length int64
	Mask   [4]byte
	Final  bool
	Opcode MessageType
}
