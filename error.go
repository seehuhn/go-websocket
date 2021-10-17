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

import "errors"

var (
	// ErrConnClosed indicates that the websocket connection has been
	// closed (either by the server or the client).
	ErrConnClosed = errors.New("connection closed")

	// ErrMessageType indicates that an invalid message type has been
	// encountered.  Valid message types are Text and Binary.
	ErrMessageType = errors.New("invalid message type")

	// ErrStatusCode indicates that an invalid status code has been
	// supplied.  Valid status codes are in the range from 1000 to
	// 4999.
	ErrStatusCode = errors.New("invalid status code")

	// ErrTooLarge is used by ReceiveBinary and ReceiveText to
	// indicate that the client sent a too large message.
	ErrTooLarge = errors.New("message too large")

	errFrameFormat = errors.New("invalid frame format")

	errHandshake = errors.New("websocket handshake failed")
)
