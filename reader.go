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
	"context"
	"io"
	"math"
	"reflect"
	"unicode/utf8"
)

type messageInfo struct {
	sizeHint    int // -1 if unknown
	messageType MessageType
}

type readResult struct {
	n     int
	final bool
}

type readMultiplexerData struct {
	newMessage chan<- messageInfo
	fromUser   <-chan []byte
	toUser     chan<- readResult

	readerDone chan<- struct{}
}

// ReadMultiplexer reads all data from the network connection.
// Control frames are handled internally by this function,
// data frames are read on behalf of the user.
func (conn *Conn) readMultiplexer(data *readMultiplexerData) {
	closeStatus := StatusOK // sent to the client when readMultiplexer exits
	check := func(err error) bool {
		if err == nil {
			return false
		}

		if err == errFrameFormat {
			closeStatus = StatusProtocolError
		} else {
			closeStatus = StatusInternalServerError
		}
		return true
	}

	needsCont := false
	scratch := make([]byte, 128) // buffer for headers and control frame payloads
	header := &header{}
readLoop:
	for {
		err := conn.readFrameHeader(header, scratch)
		if check(err) {
			break readLoop
		}

		switch header.Opcode {
		case Text, Binary, contFrame:
			if needsCont != (header.Opcode == contFrame) {
				closeStatus = StatusProtocolError
				break readLoop
			}
			needsCont = !header.Final

			if header.Opcode != contFrame {
				sizeHint := -1
				if header.Final && header.Length <= math.MaxInt {
					sizeHint = int(header.Length)
				}
				data.newMessage <- messageInfo{
					messageType: header.Opcode,
					sizeHint:    sizeHint,
				}
			}

			available := header.Length
			done := false
			for !done {
				buf := <-data.fromUser

				if buf != nil {
					amount := len(buf)
					if amount > int(available) {
						amount = int(available)
					}
					n, err := conn.rw.Read(buf[:amount])
					if check(err) {
						break readLoop
					}
					available -= int64(n)

					done = header.Final && available == 0
					data.toUser <- readResult{n, done}
				} else {
					n, err := io.CopyN(io.Discard, conn.rw, available)
					if check(err) {
						break readLoop
					}
					available -= n

					if n > math.MaxInt {
						// We only use n to detect whether any data was
						// read at all, so we can safely truncate the value.
						n = math.MaxInt
					}
					done = header.Final
					data.toUser <- readResult{int(n), done}
				}
				if available == 0 {
					break
				}
			}

		case closeFrame:
			body, err := conn.readControlFrameBody(scratch, header)
			if check(err) {
				break readLoop
			}

			clientStatus := StatusDropped
			var clientMessage string
			switch {
			case len(body) == 0:
				clientStatus = StatusNotSent
			case len(body) >= 2:
				recvStatus := 256*Status(body[0]) + Status(body[1])
				if recvStatus.clientCanSend() && utf8.Valid(body[2:]) {
					clientStatus = recvStatus
					clientMessage = string(body[2:])
				}
			}
			conn.closeMutex.Lock()
			conn.clientStatus = clientStatus
			conn.clientMessage = clientMessage
			conn.closeMutex.Unlock()

			if clientStatus.serverCanSend() {
				closeStatus = clientStatus
			} else {
				closeStatus = StatusProtocolError
			}
			break readLoop

		case pingFrame:
			body, err := conn.readControlFrameBody(scratch, header)
			if check(err) {
				break readLoop
			}

			ctl := framePool.Get().(*frame)
			ctl.Opcode = pongFrame
			ctl.Body = body
			ctl.Final = true
			conn.sendControlFrame <- ctl

		case pongFrame:
			// we don't send ping frames, so we simply swallow pong frames
			_, err := conn.readControlFrameBody(scratch, header)
			if check(err) {
				break readLoop
			}

		default:
			closeStatus = StatusProtocolError
			break readLoop
		}
	}

	// We may already have sent a close frame in the [Conn.Close] method.
	// Sending a close frame here unconditionally is safe, since the client
	// will ignore everything after it has received the first close message.
	//
	// Since we are exiting anyway, we don't care about errors here.
	conn.sendCloseFrame(closeStatus, nil)

	// Closing this will terminate the writer, so this must come after the call
	// to [Conn.sendCloseFrame].
	close(data.readerDone)

	// Finally, notify the user that no more data will be incoming.
	close(data.newMessage)
	close(data.toUser)
}

// readFrameHeader reads and decodes a frame header
func (conn *Conn) readFrameHeader(header *header, scratch []byte) error {
	_, err := io.ReadFull(conn.rw, scratch[:2])
	if err != nil {
		return err
	}

	final := scratch[0] & 128
	reserved := (scratch[0] >> 4) & 7
	if reserved != 0 {
		return errFrameFormat
	}
	opcode := scratch[0] & 15

	mask := scratch[1] & 128
	if mask == 0 {
		return errFrameFormat
	}

	// read the length
	l8 := scratch[1] & 127
	lengthBytes := 1
	if l8 == 127 {
		lengthBytes = 8
	} else if l8 == 126 {
		lengthBytes = 2
	}
	if lengthBytes > 1 {
		n, _ := io.ReadFull(conn.rw, scratch[:lengthBytes])
		if n < lengthBytes {
			return errFrameFormat
		}
	} else {
		scratch[0] = l8
	}
	var length uint64
	for i := 0; i < lengthBytes; i++ {
		length = length<<8 | uint64(scratch[i])
	}
	if length&(1<<63) != 0 {
		return errFrameFormat
	}

	if opcode >= 8 && (final == 0 || length > 125) {
		return errFrameFormat
	}

	header.Final = final != 0
	header.Opcode = MessageType(opcode)
	header.Length = int64(length)

	// read the masking key
	_, err = io.ReadFull(conn.rw, header.Mask[:])
	if err != nil {
		return err
	}

	return nil
}

func (conn *Conn) readControlFrameBody(buf []byte, header *header) ([]byte, error) {
	payloadLength := header.Length
	if payloadLength > 125 {
		// All control frames MUST have a payload length of 125 bytes or less
		// and MUST NOT be fragmented.
		return nil, errFrameFormat
	}
	n, err := io.ReadFull(conn.rw, buf[:payloadLength])
	for i := 0; i < n; i++ {
		buf[i] ^= header.Mask[i%4]
	}
	return buf[:n], err
}

// ReceiveMessage returns an io.Reader which can be used to read the next
// message from the connection.  The first return value gives the message type
// received (Text or Binary).
//
// No more messages can be received until the returned io.Reader has been
// drained.  In order to avoid deadlocks, the reader must always read the
// complete message.
func (conn *Conn) ReceiveMessage() (MessageType, io.Reader, error) {
	msgInfo, ok := <-conn.newMessage
	if !ok {
		return 0, nil, ErrConnClosed
	}

	r := &frameReader{conn: conn}

	return msgInfo.messageType, r, nil
}

// ReceiveOneMessage listens on all given connections until a new message
// arrives.  The function returns the index of the connection, the message type,
// and a reader which can be used to read the message contents.
//
// No more messages can be received on this connection until the returned
// io.Reader has been drained.  In order to avoid deadlocks, the caller must
// always read the complete message.
//
// If the context expires or is cancelled, the error is either
// context.DeadlineExceeded or context.Cancelled.
//
// If more than 65535 clients are given, the function panics.
func ReceiveOneMessage(ctx context.Context, clients []*Conn) (int, MessageType, io.Reader, error) {
	idx, msgInfo, err := selectChannel(ctx, clients)
	if err != nil {
		return -1, 0, nil, err
	}
	r := &frameReader{conn: clients[idx]}
	return idx, msgInfo.messageType, r, nil
}

func selectChannel(ctx context.Context, clients []*Conn) (int, messageInfo, error) {
	numClients := len(clients)
	if numClients > 65535 {
		// select supports at most 65536 cases, and we need one for the context
		panic("too many clients")
	}

	// set up channels for the select statement
	cases := make([]reflect.SelectCase, numClients+1)
	for i, conn := range clients {
		cases[i] = reflect.SelectCase{
			Dir:  reflect.SelectRecv,
			Chan: reflect.ValueOf(conn.newMessage),
		}
	}
	cases[numClients] = reflect.SelectCase{
		Dir:  reflect.SelectRecv,
		Chan: reflect.ValueOf(ctx.Done()),
	}

	numClosed := 0
	for {
		idx, recv, recvOK := reflect.Select(cases)

		if idx == numClients {
			// the context was cancelled
			return -1, messageInfo{}, ctx.Err()
		}

		if !recvOK {
			// the connection was closed
			numClosed++
			if numClosed == numClients {
				return -1, messageInfo{}, ErrConnClosed
			}
			cases[idx].Chan = reflect.ValueOf((<-chan messageInfo)(nil))
			continue
		}

		msgInfo := recv.Interface().(messageInfo)
		return idx, msgInfo, nil
	}
}

type frameReader struct {
	conn  *Conn
	isEOF bool
}

func (r *frameReader) Read(buf []byte) (int, error) {
	if r.isEOF {
		return 0, io.EOF
	}
	r.conn.fromUser <- buf
	res := <-r.conn.toUser
	r.isEOF = res.final
	return res.n, nil
}

// ReceiveBinary reads a binary message from the connection.  If the message
// received is not binary, the channel is closed with status StatusProtoclError
// and [ErrConnClosed] is returned.
//
// If the received message is longer than buf, the buffer contains the start of
// the message and [ErrTooLarge] is returned.
func (conn *Conn) ReceiveBinary(buf []byte) (int, error) {
	msgInfo, ok := <-conn.newMessage
	if !ok {
		return 0, ErrConnClosed
	}
	return conn.doReceiveBinary(buf, msgInfo)
}

func (conn *Conn) doReceiveBinary(buf []byte, msgInfo messageInfo) (int, error) {
	if msgInfo.messageType != Binary {
		conn.drainMessage()
		err := conn.Close(StatusProtocolError, "expected binary message")
		if err == nil {
			err = ErrConnClosed
		}
		return 0, err
	}

	pos := 0
	done := false
	for pos < len(buf) && !done {
		conn.fromUser <- buf[pos:]
		res := <-conn.toUser
		pos += res.n
		done = res.final
	}

	overflow := false
	if !done {
		overflow = conn.drainMessage()
	}

	var err error
	if overflow {
		err = ErrTooLarge
	}
	return pos, err
}

func (conn *Conn) drainMessage() bool {
	overflow := false
	for {
		conn.fromUser <- nil
		res := <-conn.toUser
		if res.n > 0 {
			overflow = true
		}
		if res.final {
			break
		}
	}
	return overflow
}

// ReceiveOneBinary listens on all given connections until a new message
// arrives, and then reads this message.  If the message received is not
// binary, the channel is closed with status StatusProtoclError and
// [ErrConnClosed] is returned.
//
// If the received message is longer than buf, the buffer contains the start of
// the message and [ErrTooLarge] is returned.
//
// If the context expires or is cancelled, the error is either
// context.DeadlineExceeded or context.Cancelled.
func ReceiveOneBinary(ctx context.Context, buf []byte, clients []*Conn) (idx, n int, err error) {
	idx, msgInfo, err := selectChannel(ctx, clients)
	if err != nil {
		return -1, 0, err
	}
	n, err = clients[idx].doReceiveBinary(buf, msgInfo)
	return idx, n, err
}

// ReceiveText reads a text message from the connection.  If the next received
// message is not a text message, , the channel is closed with status
// StatusProtoclError and [ErrConnClosed] is returned.  If the length of the
// utf-8 representation of the text exceeds maxLength bytes, the text is
// truncated and ErrTooLarge is returned.
func (conn *Conn) ReceiveText(maxLength int) (string, error) {
	msgInfo, ok := <-conn.newMessage
	if !ok {
		return "", ErrConnClosed
	}
	return conn.doReceiveText(maxLength, msgInfo)
}

func (conn *Conn) doReceiveText(maxLength int, msgInfo messageInfo) (string, error) {
	if msgInfo.messageType != Text {
		conn.drainMessage()
		err := conn.Close(StatusProtocolError, "expected text message")
		if err == nil {
			err = ErrConnClosed
		}
		return "", err
	}

	if msgInfo.sizeHint >= 0 && msgInfo.sizeHint < maxLength {
		maxLength = msgInfo.sizeHint
	}
	buf := make([]byte, maxLength)

	pos := 0
	done := false
	for pos < len(buf) && !done {
		conn.fromUser <- buf[pos:]
		res := <-conn.toUser
		pos += res.n
		done = res.final
	}

	overflow := false
	if !done {
		overflow = conn.drainMessage()
	}

	// check for incomplete/invalid utf-8
	idx := 0
	for idx < pos {
		r, size := utf8.DecodeRune(buf[idx:pos])
		if r == utf8.RuneError {
			if overflow && idx > pos-utf8.UTFMax && utf8.RuneStart(buf[idx]) {
				// the last rune might be incomplete
				pos = idx
				break
			}

			err := conn.Close(StatusProtocolError, "invalid utf-8")
			if err == nil {
				err = ErrConnClosed
			}
			return "", err
		}
		idx += size
	}

	var err error
	if overflow {
		err = ErrTooLarge
	}
	return string(buf[:pos]), err
}

func ReceiveOneText(ctx context.Context, maxLength int, clients []*Conn) (idx int, text string, err error) {
	idx, msgInfo, err := selectChannel(ctx, clients)
	if err != nil {
		return -1, "", err
	}
	text, err = clients[idx].doReceiveText(maxLength, msgInfo)
	return idx, text, err
}
