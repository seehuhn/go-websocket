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
	"crypto/sha1"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"strings"
)

// Handler implements the http.Handler interface.  The handler
// responds to requests by opening a websocket connection.
type Handler struct {
	// OriginAllowed can be set to a function which returns true
	// if access should be allowed for the given value of the Origin http
	// header.
	//
	// If OriginAllowed is not set, a same-origin policy is used.
	OriginAllowed func(origin *url.URL) bool

	// AccessAllowed can be set to a function which determines whether
	// the given request is allowed to establish a WebSocket connection
	// (true indicates that the request should go ahead, false indicates
	// that the request should be blocked).
	// In addition, the function can return information from the request
	// (e.g. login details extracted from cookies).  The returned value is
	// then stored in the Conn.RequestData field.
	AccessAllowed func(r *http.Request) (bool, interface{})

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

	// The websocket sub-protocols that the server implements, in decreasing
	// order of preference.  The server selects the first possible value from
	// this list, or null (no Sec-WebSocket-Protocol header sent) if none of
	// the client-requested subprotocols are supported.
	Subprotocols []string

	// AccessOK should not be used in new code.
	//
	// Deprecated: Use OriginAllowed and/or AccessAllowed instead.
	AccessOk func(conn *Conn, protocols []string) bool
}

const websocketGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11" // from RFC 6455

func (handler *Handler) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	conn, _ := handler.Upgrade(w, req)

	// start the user handler
	handler.Handle(conn)
}

// Upgrade upgrades an HTTP connection to the websocket protocol.
// After this function returns, `w` and `req` cannot be used any more
// (even in the case of an error).
func (handler *Handler) Upgrade(w http.ResponseWriter, req *http.Request) (*Conn, error) {
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return nil, errors.New("connection hijacking not supported")
	}

	conn, status := handler.handshake(w, req)
	if status == http.StatusSwitchingProtocols {
		w.WriteHeader(status)
	} else {
		http.Error(w, "websocket handshake failed", status)
		return nil, errHandshake
	}
	raw, rw, err := hijacker.Hijack()
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return nil, err
	}
	conn.raw = raw
	conn.rw = rw

	// start the write multiplexer
	writerReady := make(chan struct{})
	go conn.writeMultiplexer(writerReady)
	<-writerReady

	// start the read multiplexer
	readerReady := make(chan struct{})
	go conn.readMultiplexer(readerReady)
	<-readerReady

	return conn, nil
}

func (handler *Handler) handshake(w http.ResponseWriter, req *http.Request) (*Conn, int) {
	if req.Method != "GET" {
		return nil, http.StatusBadRequest
	}
	version := req.Header.Get("Sec-Websocket-Version")
	if version != "13" {
		return nil, http.StatusUpgradeRequired
	}
	if strings.ToLower(req.Header.Get("Upgrade")) != "websocket" {
		return nil, http.StatusBadRequest
	}
	connection := strings.ToLower(req.Header.Get("Connection"))
	if !strings.Contains(connection, "upgrade") {
		return nil, http.StatusBadRequest
	}
	key := req.Header.Get("Sec-Websocket-Key")
	if key == "" {
		return nil, http.StatusBadRequest
	}

	// protect against CSRF attacks
	var originURI *url.URL
	origin := req.Header.Get("Origin")
	if origin != "" {
		var err error
		originURI, err = url.ParseRequestURI(origin)
		if err != nil {
			return nil, http.StatusBadRequest
		}
	}
	var originAllowed bool
	if handler.OriginAllowed != nil {
		originAllowed = handler.OriginAllowed(originURI)
	} else {
		originAllowed = originURI == nil || originURI.Host == req.Host
	}
	if !originAllowed {
		return nil, http.StatusForbidden
	}

	conn := &Conn{
		RemoteAddr: req.RemoteAddr,
	}

	if handler.AccessAllowed != nil {
		ok, data := handler.AccessAllowed(req)
		if !ok {
			return nil, http.StatusForbidden
		}
		conn.RequestData = data
	}

	origURI, err := url.ParseRequestURI(req.RequestURI)
	if err == nil {
		path := origURI.Path
		if path == "" {
			path = "/"
		}
		query := origURI.RawQuery
		if query != "" {
			query = "&" + query
		}
		conn.ResourceName = path + query
	}

	var clientProtos []string
	for _, protocol := range req.Header["Sec-Websocket-Protocol"] {
		pp := strings.Split(protocol, ",")
		for i := 0; i < len(pp); i++ {
			p := strings.TrimSpace(pp[i])
			if p != "" {
				clientProtos = append(clientProtos, p)
			}
		}
	}
	if len(clientProtos) > 0 {
		protoMap := make(map[string]bool)
		for _, p := range clientProtos {
			protoMap[p] = true
		}
		for _, p := range handler.Subprotocols {
			if protoMap[p] {
				conn.Protocol = p
				break
			}
		}
	}

	if handler.AccessAllowed == nil && handler.AccessOk != nil {
		// handle legacy clients
		ok := handler.AccessOk(conn, clientProtos)
		if !ok {
			return nil, http.StatusForbidden
		}
	}

	h := sha1.New()
	h.Write([]byte(key))
	h.Write([]byte(websocketGUID))
	accept := base64.StdEncoding.EncodeToString(h.Sum(nil))

	headers := w.Header()
	headers.Set("Sec-WebSocket-Version", "13")
	if conn.Protocol != "" {
		headers.Set("Sec-WebSocket-Protocol", conn.Protocol)
	}
	headers.Set("Upgrade", "websocket")
	headers.Set("Connection", "Upgrade")
	headers.Set("Sec-WebSocket-Accept", accept)
	if handler.ServerName != "" {
		headers.Set("Server", handler.ServerName)
	}
	return conn, http.StatusSwitchingProtocols
}
