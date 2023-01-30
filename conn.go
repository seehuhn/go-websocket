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

	senderStore chan *sender

	// ReaderDone is closed when the reader goroutine has finished.
	// After this point, the reader will not access the Conn object
	// any more and will not send any more control messages.
	readerDone <-chan struct{}
	toUser     <-chan *reader
	fromUser   chan<- *reader

	// the following fields can only be read once readerDone is closed
	connInfo      ConnInfo
	clientStatus  Status
	clientMessage string
}

// ConnInfo describes why a websocket connection was closed.
type ConnInfo int

const (
	_ ConnInfo = iota

	// ServerClosed indicates that [Conn.Close] was called.
	ServerClosed

	// ClientClosed indicates that the client closed the connection by
	// sending a close frame.
	ClientClosed

	// ProtocolViolation indicates that we closed the connection because
	// because the client sent invalid data.
	ProtocolViolation

	// WrongMessageType indicates that we closed the connection because
	// the client sent a message of the wrong type.
	WrongMessageType

	// ConnDropped indicates that the underlying TCP connection was
	// closed, and we didn't receive a close frame from the client.
	ConnDropped
)

// Status describes the reason for the closure of a websocket connection, for
// use in the Conn.Close() method.
type Status uint16

// Websocket status codes as defined in RFC 6455.
// In addition to the predefined codes, applications can also use
// codes in the range 3000-4999.
// See: https://tools.ietf.org/html/rfc6455#section-7.4.1
const (
	// StatusOK indicates that the connection was closed normally.
	StatusOK Status = 1000

	// StatusGoingAway indicates that an endpoint is "going away", such as a
	// server going down or a browser having navigated away from a page.
	StatusGoingAway Status = 1001

	// StatusProtocolError indicates that an endpoint is terminating the
	// connection due to a protocol error.
	StatusProtocolError Status = 1002

	// StatusUnsupportedType indicates that an endpoint is terminating the
	// connection because it has received a type of data it cannot accept
	// (e.g., an endpoint that understands only text data MAY send this if
	// it receives a binary message).
	StatusUnsupportedType Status = 1003

	// StatusNotSent indicates that no status code is present.
	StatusNotSent Status = 1005 // never sent over the wire

	// StatusDropped indicates that the connection was dropped without
	// receiving a close frame.
	StatusDropped Status = 1006 // never sent over the wire

	// StatusInvalidData indicates that an endpoint is terminating the
	// connection because it has received data within a message that was\
	// not consistent with the type of the message (e.g., non-UTF-8 data
	// within a text message).
	StatusInvalidData Status = 1007

	// StatusPolicyViolation indicates that an endpoint is terminating the
	// connection because it has received a message that violates its policy.
	// This is a generic status code that can be returned when there is no
	// other more suitable status code or if there is a need to hide specific
	// details about the policy.
	StatusPolicyViolation Status = 1008

	// StatusTooLarge indicates that an endpoint is terminating the
	// connection because it has received a message that is too big for it
	// to process.
	StatusTooLarge Status = 1009

	// StatusClientMissingExtension indicates that the client is terminating
	// the connection because it expected the server to negotiate one or
	// more extensions, but the server did not accept these extensions.
	StatusClientMissingExtension Status = 1010 // only sent by client

	// StatusInternalServerError indicates that an endpoint is terminating
	// the connection because it encountered an unexpected condition that
	// prevented it from fulfilling the request.
	StatusInternalServerError Status = 1011
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

// Wait blocks until the connection is closed.  The function then returns the
// information about the connection, the status code and the message the client
// sent when closing the connection.
//
// If no valid close frame was received from the client, the status code will
// be StatusDropped.  If we received a close frame, but no status code was
// included, the status code will be StatusNotSent.  Otherwise, the status code
// is the status code sent by the client.
func (conn *Conn) Wait() (ConnInfo, Status, string) {
	<-conn.readerDone
	return conn.connInfo, conn.clientStatus, conn.clientMessage
}

type frameHeader struct {
	Length int64
	Mask   [4]byte
	Final  bool
	Opcode MessageType
}

// MessageType encodes the type of an individual websocket message.
type MessageType byte

// Websocket message types.
const (
	Text   MessageType = 1
	Binary MessageType = 2

	// The following frame types are only used internally.
	// See: https://tools.ietf.org/html/rfc6455#section-5.6
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
