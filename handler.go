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
	"time"
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
	// stored in the [Conn.RequestData] field.
	AccessAllowed func(r *http.Request) (bool, interface{})

	// Handle is called after the websocket handshake has completed
	// successfully and the object conn can be used to send and
	// receive messages on the connection.
	//
	// The connection object conn can be passed to other parts of the
	// programm, and will stay functional even after the call to
	// Handle is complete.  Use [conn.Close] to close the connection
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
}

const websocketGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11" // from RFC 6455

func (handler *Handler) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	conn, err := handler.Upgrade(w, req)
	if err != nil {
		return
	}

	// start the user handler
	handler.Handle(conn)
}

// Upgrade upgrades an HTTP connection to the websocket protocol.
// After this function returns, `w` and `req` cannot be used any more.
func (handler *Handler) Upgrade(w http.ResponseWriter, req *http.Request) (*Conn, error) {
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return nil, errors.New("connection hijacking not supported")
	}

	conn, status := handler.handshake(w, req)
	if status != http.StatusSwitchingProtocols {
		http.Error(w, "websocket handshake failed", status)
		return nil, errHandshake
	}

	w.WriteHeader(status)
	raw, rw, err := hijacker.Hijack()
	if err != nil {
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return nil, err
	}
	raw.SetDeadline(time.Time{})

	conn.initialize(raw, rw)

	return conn, nil
}

func (handler *Handler) handshake(w http.ResponseWriter, req *http.Request) (*Conn, int) {
	// This code is organised following the steps in section 4.2 of RFC 6455,
	// see https://www.rfc-editor.org/rfc/rfc6455#section-4.2 .

	// The method of the request MUST be GET, and the HTTP version MUST be at
	// least 1.1.
	if req.Method != "GET" || req.ProtoMajor == 1 && req.ProtoMinor == 0 {
		return nil, http.StatusBadRequest
	}

	var resourceName string
	origURI, err := url.ParseRequestURI(req.RequestURI)
	if err != nil {
		return nil, http.StatusBadRequest
	}
	path := origURI.Path
	if path == "" {
		path = "/"
	}
	query := origURI.RawQuery
	if query != "" {
		query = "&" + query
	}
	resourceName = path + query

	// The request MUST contain an |Upgrade| header field whose value MUST
	// include the "websocket" keyword.
	if !containsTokenFold(req.Header.Values("Upgrade"), "websocket") {
		return nil, http.StatusBadRequest
	}

	// The request MUST contain a |Connection| header field whose value MUST
	// include the "Upgrade" token.
	if !containsTokenFold(req.Header.Values("Connection"), "upgrade") {
		return nil, http.StatusBadRequest
	}

	// The request MUST include a header field with the name
	// |Sec-WebSocket-Key|.
	secWebsocketKey := req.Header.Get("Sec-Websocket-Key")
	if secWebsocketKey == "" {
		return nil, http.StatusBadRequest
	}

	// The request MUST include a header field with the name
	// |Sec-WebSocket-Version|.  The value of this header field MUST be 13.
	version := req.Header.Get("Sec-Websocket-Version")
	if version != "13" {
		headers := w.Header()
		headers.Set("Upgrade", "websocket")
		headers.Set("Connection", "Upgrade")
		headers.Set("Sec-WebSocket-Version", "13")
		return nil, http.StatusUpgradeRequired
	}

	subprotocol := handler.chooseSubprotocol(req)

	// protect against CSRF attacks
	var origin *url.URL
	if origins := req.Header.Values("Origin"); len(origins) > 0 {
		originURI, err := url.ParseRequestURI(origins[0])
		if err != nil {
			return nil, http.StatusBadRequest
		}
		origin = originURI

		var originAllowed bool
		if handler.OriginAllowed != nil {
			originAllowed = handler.OriginAllowed(origin)
		} else {
			originAllowed = strings.EqualFold(origin.Host, req.Host)
		}
		if !originAllowed {
			return nil, http.StatusForbidden
		}
	}

	// access control
	var requestData interface{}
	if handler.AccessAllowed != nil {
		ok, data := handler.AccessAllowed(req)
		if !ok {
			return nil, http.StatusForbidden
		}
		requestData = data
	}

	// if we reach this point, we accept the connection

	conn := &Conn{
		ResourceName: resourceName,
		Origin:       origin,
		RemoteAddr:   req.RemoteAddr,
		Protocol:     subprotocol,
		RequestData:  requestData,
	}

	h := sha1.New()
	h.Write([]byte(secWebsocketKey))
	h.Write([]byte(websocketGUID))
	secWebsocketAccept := base64.StdEncoding.EncodeToString(h.Sum(nil))

	headers := w.Header()
	headers.Set("Upgrade", "websocket")
	headers.Set("Connection", "Upgrade")
	headers.Set("Sec-WebSocket-Accept", secWebsocketAccept)
	if subprotocol != "" {
		headers.Set("Sec-WebSocket-Protocol", subprotocol)
	}
	if handler.ServerName != "" {
		headers.Set("Server", handler.ServerName)
	}

	return conn, http.StatusSwitchingProtocols
}

func (handler *Handler) chooseSubprotocol(req *http.Request) string {
	serverProtos := handler.Subprotocols
	if len(serverProtos) == 0 {
		return ""
	}

	var clientProtos []string
	pp := strings.Split(req.Header.Get("Sec-Websocket-Protocol"), ",")
	for i := 0; i < len(pp); i++ {
		p := strings.TrimSpace(pp[i])
		if p != "" {
			clientProtos = append(clientProtos, p)
		}
	}

	for _, p := range serverProtos {
		for _, q := range clientProtos {
			if p == q {
				return p
			}
		}
	}
	return ""
}

// containsTokenFold reports whether s contains a given token.
// The comparison is case-insensitive.
// token must be lower case.
func containsTokenFold(headers []string, token string) bool {
	for _, s := range headers {
		pos := 0
		n := len(s)

		// skip to the first token
		for pos < n && !isTokenByte[s[pos]] {
			pos++
		}

		for pos < n {
			start := pos
			for pos < n && isTokenByte[s[pos]] {
				pos++
			}
			if strings.ToLower(s[start:pos]) == token {
				return true
			}

			for pos < n && !isTokenByte[s[pos]] {
				pos++
			}
		}
	}
	return false
}

var isTokenByte = map[byte]bool{
	'!':  true,
	'#':  true,
	'$':  true,
	'%':  true,
	'&':  true,
	'\'': true,
	'*':  true,
	'+':  true,
	'-':  true,
	'.':  true,
	'0':  true,
	'1':  true,
	'2':  true,
	'3':  true,
	'4':  true,
	'5':  true,
	'6':  true,
	'7':  true,
	'8':  true,
	'9':  true,
	'A':  true,
	'B':  true,
	'C':  true,
	'D':  true,
	'E':  true,
	'F':  true,
	'G':  true,
	'H':  true,
	'I':  true,
	'J':  true,
	'K':  true,
	'L':  true,
	'M':  true,
	'N':  true,
	'O':  true,
	'P':  true,
	'Q':  true,
	'R':  true,
	'S':  true,
	'T':  true,
	'U':  true,
	'V':  true,
	'W':  true,
	'X':  true,
	'Y':  true,
	'Z':  true,
	'^':  true,
	'_':  true,
	'`':  true,
	'a':  true,
	'b':  true,
	'c':  true,
	'd':  true,
	'e':  true,
	'f':  true,
	'g':  true,
	'h':  true,
	'i':  true,
	'j':  true,
	'k':  true,
	'l':  true,
	'm':  true,
	'n':  true,
	'o':  true,
	'p':  true,
	'q':  true,
	'r':  true,
	's':  true,
	't':  true,
	'u':  true,
	'v':  true,
	'w':  true,
	'x':  true,
	'y':  true,
	'z':  true,
	'|':  true,
	'~':  true,
}
