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
	"crypto/sha1"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Conn represents a websocket connection initiated by a client.  All
// fields are read-only.  It is ok to access a Conn from different
// goroutines concurrently.  The connection must be closed using the
// Close() method after use, to free all allocated resources.
//
// Use a Handler to obtain Conn objects.
type Conn struct {
	ResourceName *url.URL
	Origin       *url.URL
	RemoteAddr   string
	Protocol     string

	raw net.Conn
	rw  *bufio.ReadWriter

	readerDone <-chan struct{}
	writerDone <-chan struct{}

	getDataReader    <-chan *frameReader
	getDataWriter    <-chan *frameWriter
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

	invalidFrame MessageType = 255 // internal use only
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
	StatusOK                  Status = 1000
	StatusGoingAway           Status = 1001
	StatusProtocolError       Status = 1002
	StatusUnsupportedType     Status = 1003
	StatusNotSent             Status = 1005
	StatusDropped             Status = 1006
	StatusInvalidData         Status = 1007
	StatusPolicyViolation     Status = 1008
	StatusTooLarge            Status = 1009
	StatusInternalServerError Status = 1011
)

func (code Status) isValid() bool {
	if code >= 3000 && code < 5000 {
		return true
	}
	return knownValidCode[code]
}

var knownValidCode = map[Status]bool{
	StatusOK:                  true,
	StatusGoingAway:           true,
	StatusProtocolError:       true,
	StatusUnsupportedType:     true,
	StatusInvalidData:         true,
	StatusPolicyViolation:     true,
	StatusTooLarge:            true,
	1010:                      true, // never sent by server
	StatusInternalServerError: true,
}

type header struct {
	Length uint64
	Mask   []byte
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

func (conn *Conn) handshake(w http.ResponseWriter, req *http.Request,
	handler *Handler) (status int, message string) {

	headers := w.Header()

	version := req.Header.Get("Sec-Websocket-Version")
	if version != "13" {
		headers.Set("Sec-WebSocket-Version", "13")
		return http.StatusUpgradeRequired, "unknown version"
	}
	if strings.ToLower(req.Header.Get("Upgrade")) != "websocket" {
		return http.StatusBadRequest, "missing upgrade header"
	}
	connection := strings.ToLower(req.Header.Get("Connection"))
	if !strings.Contains(connection, "upgrade") {
		return http.StatusBadRequest, "missing connection header"
	}
	key := req.Header.Get("Sec-Websocket-Key")
	if key == "" {
		return http.StatusBadRequest, "missing Sec-Websocket-Key"
	}

	var scheme string
	if req.TLS != nil {
		scheme = "wss"
	} else {
		scheme = "ws"
	}
	resourceName, err := url.ParseRequestURI(scheme + "://" + req.Host + req.URL.RequestURI())
	if err != nil {
		return http.StatusBadRequest, "invalid Request-URI"
	}
	conn.ResourceName = resourceName

	var origin *url.URL
	originString := req.Header.Get("Origin")
	if originString != "" {
		origin, err = url.ParseRequestURI(originString)
		if err != nil {
			return http.StatusBadRequest, "invalid Origin"
		}
	}
	conn.Origin = origin
	conn.RemoteAddr = req.RemoteAddr

	var protocols []string
	protocol := strings.Join(req.Header["Sec-Websocket-Protocol"], ",")
	if protocol != "" {
		pp := strings.Split(protocol, ",")
		for i := 0; i < len(pp); i++ {
			p := strings.TrimSpace(pp[i])
			if p != "" {
				protocols = append(protocols, p)
			}
		}
	}

	if handler.AccessOk != nil {
		ok := handler.AccessOk(conn, protocols)
		if !ok {
			return http.StatusForbidden, "not allowed"
		}
	}

	h := sha1.New()
	h.Write([]byte(key))
	h.Write([]byte(websocketGUID))
	accept := base64.StdEncoding.EncodeToString(h.Sum(nil))

	if conn.Protocol != "" {
		headers.Set("Sec-WebSocket-Protocol", conn.Protocol)
	}
	headers.Set("Upgrade", "websocket")
	headers.Set("Connection", "Upgrade")
	headers.Set("Sec-WebSocket-Accept", accept)
	if handler.ServerName != "" {
		headers.Set("Server", handler.ServerName)
	}
	return http.StatusSwitchingProtocols, ""
}

func (conn *Conn) sendCloseFrame(status Status, body []byte) error {
	var buf []byte
	if status != 0 && status != StatusNotSent {
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

// Close terminates a websocket connection and frees all associated
// resources.  The connection cannot be used any more after Close()
// has been called.
//
// The status code indicates whether the connection completed
// successfully, or due to an error.  Use StatusOK for normal
// termination, and one of the other status codes in case of errors.
// Use StatusNotSent to not send a status code.
//
// The message can be used to provide additional information for
// debugging.  The utf-8 representation of the string can be at most
// 123 bytes long, otherwise ErrTooLarge is returned.
func (conn *Conn) Close(code Status, message string) error {
	if !code.isValid() || code == 1010 {
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

	needsClose := true
	timeOut := time.NewTimer(3 * time.Second)
	select {
	case <-conn.readerDone:
		if !timeOut.Stop() {
			<-timeOut.C
		}
	case <-timeOut.C:
		conn.raw.Close()
		needsClose = false
	}

	// The reader uses conn.sendControlFrame, so we must be sure the
	// reader has stopped before we can stop the writer (by closing
	// the channel).
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
