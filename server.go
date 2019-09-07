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
	"net/http"
)

// Handler implements the http.Handler interface.  The handler
// responds to requests, by opening a websocket connection.
type Handler struct {
	// AccessOK, if non-nil, is called during the opening handshake to
	// decide whether the client is allowed to access the service.  At
	// the time this function is called, the connection is not yet
	// functional, but the fields ResourceName (describing the URL the
	// client used to access the server) and Origin (as reported by
	// the web browser) are already initialised.
	//
	// If the client sends a list of supported sub-protocols, these
	// will be available in protocols.  In this case, AccessOk must
	// assign one of the supported sub-protocols to the Protocol field
	// of conn.  Otherwise the connection setup will fail.
	// See: https://tools.ietf.org/html/rfc6455#section-1.9
	//
	// The function must return true, if the client is allowed to
	// access the service, and false otherwise.
	AccessOk func(conn *Conn, protocols []string) bool

	// Handle is called after the websocket handshake has completed
	// successfully, and the object conn can be used to send and
	// receive messages on the connection.
	//
	// The connection object conn can be passed to other parts of the
	// programm, and will stay functional even after the call to
	// Handle is complete.  Use conn.Close() to close the connection
	// after use.
	Handle func(conn *Conn)

	// If non-empty, this string is sent in the "Server" HTTP header
	// during handshake.
	ServerName string
}

func (handler *Handler) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hijacking not supported",
			http.StatusInternalServerError)
		return
	}

	conn := &Conn{}
	status, msg := conn.handshake(w, req, handler)
	if status == http.StatusSwitchingProtocols {
		w.WriteHeader(status)
	} else {
		http.Error(w, msg, status)
		return
	}
	raw, rw, err := hijacker.Hijack()
	if err != nil {
		http.Error(w, "hijacking failed", http.StatusInternalServerError)
		return
	}
	conn.raw = raw
	conn.rw = rw

	// TODO(voss): should the rest of this function be moved into a goroutine
	// and the ServeHTTP method allowed to finish early?

	// start the write multiplexer
	writerReady := make(chan struct{})
	go conn.writeMultiplexer(writerReady)
	<-writerReady

	// start the read multiplexer
	readerReady := make(chan struct{})
	go conn.readMultiplexer(readerReady)
	<-readerReady

	// start the user handler
	handler.Handle(conn)
}
