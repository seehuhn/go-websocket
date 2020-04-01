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

type webSocketError string

func (err webSocketError) Error() string {
	return string(err)
}

const (
	// ErrConnClosed indicates that the websocket connection has been
	// closed (either by the server or the client).
	ErrConnClosed = webSocketError("connection closed")

	// ErrMessageType indicates that an invalid message type has been
	// encountered.  Valid message types are Text and Binary.
	ErrMessageType = webSocketError("invalid message type")

	// ErrStatusCode indicates that an invalid status code has been
	// supplied.  Valid status codes are in the range from 1000 to
	// 4999.
	ErrStatusCode = webSocketError("invalid status code")

	// ErrTooLarge is used by ReceiveBinary and ReceiveText to
	// indicate that the client sent a too large message.
	ErrTooLarge = webSocketError("message too large")

	errFrameFormat = webSocketError("invalid frame format")
)
