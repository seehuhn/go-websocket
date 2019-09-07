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

/*
Package websocket implements an HTTP server to accept websocket connections.

To accept websocket connections from clients, use a websocket.Handler
object:

	websocketHandler := &websocket.Handler{
		Handle: myHandler,
	}
	http.Handle("/api/ws", websocketHandler)

The function myHandler is called every time a client requests a new
websocket.  The websocket.Conn object passed to handler can be used
to send and receive messages.  The handler must close the connection
when finished with it:

	func myHandler(conn *websocket.Conn) {
		defer conn.Close(websocket.StatusOK, "")

		// use conn to send and receive messages.
	}

*/
package websocket
