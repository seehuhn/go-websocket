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
	"context"
	"io"
	"reflect"
	"unicode/utf8"
)

type reader struct {
	r           *bufio.Reader
	senderStore chan *sender // only used for pong frames
	scratch     []byte       // buffer for headers and control frame payloads
	header      frameHeader
	pos         int64
	connInfo    ConnInfo
}

type readMultiplexerData struct {
	fromUser   <-chan *reader
	toUser     chan<- *reader
	readerDone chan<- struct{}
}

// ReadMultiplexer reads all data from the network connection.
// Control frames are handled internally by this function,
// data frames are read on behalf of the user.
func (conn *Conn) readMultiplexer(data *readMultiplexerData) {
	var rb *reader
	for {
		rb = <-data.fromUser
		if rb.connInfo != 0 {
			break
		}

		// Wait until a new data frame is available.
		// We don't need to check the returned error value, since in case
		// of error, rb.connInfo is non-zero or rb.header.Opcode == closeFrame.
		rb.refill(false)
		if rb.connInfo != 0 || rb.header.Opcode == closeFrame {
			break
		}
		data.toUser <- rb
	}

	// Notify the user that no more data will be incoming.
	close(data.toUser)

	// reason == 0, close
	// reason != 0, close
	// reason != 0, other

	// Determine the client status code and message.
	connInfo := rb.connInfo
	clientStatus := StatusDropped
	var clientMessage string
	if connInfo == 0 && rb.header.Opcode == closeFrame {
		body := rb.scratch[:rb.header.Length]
		switch len(body) {
		case 0:
			clientStatus = StatusNotSent
		case 1:
			connInfo = ProtocolViolation
		default:
			tmp := 256*Status(body[0]) + Status(body[1])
			if tmp.clientCanSend() && utf8.Valid(body[2:]) {
				clientStatus = tmp
				clientMessage = string(body[2:])
			} else {
				connInfo = ProtocolViolation
			}
		}
	}

	wb := <-conn.senderStore
	if wb != nil {
		// We haven't sent a close frame yet, so we can send one now.

		// no more frames are sent after the close frame
		close(conn.senderStore)

		var closeStatus Status
		if connInfo == 0 {
			closeStatus = clientStatus
		} else if connInfo == WrongMessageType {
			closeStatus = StatusUnsupportedType
		} else {
			closeStatus = StatusProtocolError
		}

		// TODO(voss): what to do in case of send errors?
		wb.sendCloseFrame(closeStatus, nil)

		if connInfo == 0 {
			connInfo = ClientClosed
		}
	} else if connInfo == 0 {
		connInfo = ServerClosed
	}

	// Close the TCP connection.
	// The connection may already be closed at this point, but since we ignore
	// errors here, this is not a problem.
	conn.raw.Close()

	conn.connInfo = connInfo
	conn.clientStatus = clientStatus
	conn.clientMessage = clientMessage
	close(data.readerDone)
}

// Refill reads data from the connection until a data frame is available.
// Control frames are processed as they are encountered.
// If an error is returned, rb.connInfo is set to the appropriate value.
func (rb *reader) refill(isCont bool) error {
	if rb.header.Opcode == closeFrame {
		return ErrConnClosed
	}
	for {
		err := rb.readFrameHeader()
		if err != nil {
			if err == errFrameFormat {
				rb.connInfo = ProtocolViolation
			} else {
				rb.connInfo = ConnDropped
			}
			return err
		}

		if rb.header.Opcode >= 8 { // control frame
			if rb.header.Length > 125 {
				// All control frames MUST have a payload length of 125 bytes or less
				// and MUST NOT be fragmented.
				rb.connInfo = ProtocolViolation
				return ErrConnClosed
			}
			_, err = io.ReadFull(rb.r, rb.scratch[:rb.header.Length])
			if err != nil {
				rb.connInfo = ConnDropped
				return err
			}
			rb.unmask(rb.scratch[:rb.header.Length])
		}

		switch rb.header.Opcode {
		case Text, Binary:
			if isCont {
				rb.connInfo = ProtocolViolation
				return ErrConnClosed
			}
			return nil

		case contFrame:
			if !isCont {
				rb.connInfo = ProtocolViolation
				return ErrConnClosed
			}
			return nil

		case closeFrame:
			return ErrConnClosed

		case pingFrame:
			// TODO(voss): can we make this less ugly?
			// TODO(voss): what to do if there is an error sending the pong?
			body := make([]byte, rb.header.Length)
			copy(body, rb.scratch[:rb.header.Length])
			select {
			case wb := <-rb.senderStore:
				// If the sender is available, send the pong frame immediately.
				if wb != nil {
					wb.sendFrame(pongFrame, body, true)
					rb.senderStore <- wb
				}
			default:
				// Otherwise, send the pong frame in a separate goroutine.
				go func() {
					wb := <-rb.senderStore
					if wb != nil {
						wb.sendFrame(pongFrame, body, true)
						rb.senderStore <- wb
					}
				}()
			}

		case pongFrame:
			// we don't send ping frames and we ignore pong frames

		default:
			rb.connInfo = ProtocolViolation
			return ErrConnClosed
		}
	}
}

func (b *reader) readFrameHeader() error {
	b0, err := b.r.ReadByte()
	if err != nil {
		return err
	}
	b1, err := b.r.ReadByte()
	if err != nil {
		return err
	}

	final := b0 & 128
	reserved := b0 & (7 << 4)
	if reserved != 0 {
		return errFrameFormat
	}
	opcode := b0 & 15

	mask := b1 & 128
	if mask == 0 {
		return errFrameFormat
	}

	// read the length
	l8 := b1 & 127
	lengthBytes := 1
	if l8 == 127 {
		lengthBytes = 8
	} else if l8 == 126 {
		lengthBytes = 2
	}
	if lengthBytes > 1 {
		n, _ := io.ReadFull(b.r, b.scratch[:lengthBytes])
		if n < lengthBytes {
			return errFrameFormat
		}
	} else {
		b.scratch[0] = l8
	}
	var length uint64
	for i := 0; i < lengthBytes; i++ {
		length = length<<8 | uint64(b.scratch[i])
	}
	if length&(1<<63) != 0 {
		return errFrameFormat
	}

	if opcode >= 8 && (final == 0 || length > 125) {
		return errFrameFormat
	}

	b.header.Final = final != 0
	b.header.Opcode = MessageType(opcode)
	b.header.Length = int64(length)

	// read the masking key
	_, err = io.ReadFull(b.r, b.header.Mask[:])
	if err != nil {
		return err
	}

	b.pos = 0

	return nil
}

func (b *reader) unmask(buf []byte) {
	for i := range buf {
		buf[i] ^= b.header.Mask[b.pos&3]
		b.pos++
	}
}

type frameReader struct {
	rb       *reader
	fromUser chan<- *reader
}

func (fr *frameReader) Read(buf []byte) (int, error) {
	rb := fr.rb
	for rb.pos >= rb.header.Length && !rb.header.Final {
		err := rb.refill(true)
		if err != nil {
			return 0, err
		}
	}
	// now there is either data available, or b.final is set (or both)

	amount := len(buf)
	if int64(amount) > rb.header.Length-rb.pos {
		amount = int(rb.header.Length - rb.pos)
	}
	n, err := rb.r.Read(buf[:amount])
	rb.unmask(buf[:n])
	if err != nil {
		rb.connInfo = ConnDropped
		return n, err
	}

	if rb.pos >= rb.header.Length && rb.header.Final {
		err = io.EOF
	}

	return n, err
}

func (fr *frameReader) ReadAll(buf []byte) (int, error) {
	n := 0
	for n < len(buf) {
		k, err := fr.Read(buf[n:])
		n += k
		if err == io.EOF {
			return n, nil
		} else if err != nil {
			return n, err
		}
	}

	k, err := io.Copy(io.Discard, fr)
	if err != nil {
		return n, err
	}
	if k > 0 {
		err = ErrTooLarge
	}
	return n, err
}

type autoCloseReader struct {
	fr  *frameReader
	err error
}

func (ac *autoCloseReader) Read(buf []byte) (int, error) {
	if ac.err != nil {
		return 0, ac.err
	}

	fr := ac.fr
	n, err := fr.Read(buf)
	if err != nil {
		ac.err = err
		fr.fromUser <- fr.rb
	}
	return n, err
}

// ReceiveMessage returns an io.Reader which can be used to read the next
// message from the connection.  The first return value gives the message type
// received (Text or Binary).
//
// No more messages can be received until the returned io.Reader has been
// drained.  In order to avoid deadlocks, the reader must always read the
// complete message.
func (conn *Conn) ReceiveMessage() (MessageType, io.Reader, error) {
	b, ok := <-conn.toUser
	if !ok {
		return 0, nil, ErrConnClosed
	}

	fr := &frameReader{rb: b, fromUser: conn.fromUser}
	ac := &autoCloseReader{fr: fr}

	return b.header.Opcode, ac, nil
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
	idx, b, err := selectChannel(ctx, clients)
	if err != nil {
		return -1, 0, nil, err
	}

	fr := &frameReader{rb: b, fromUser: clients[idx].fromUser}
	ac := &autoCloseReader{fr: fr}

	return idx, b.header.Opcode, ac, nil
}

// ReceiveBinary reads a binary message from the connection.  If the message
// received is not binary, the channel is closed with status
// StatusProtocolError and [ErrConnClosed] is returned.
//
// If the received message is longer than buf, the buffer contains the start of
// the message and [ErrTooLarge] is returned.
func (conn *Conn) ReceiveBinary(buf []byte) (int, error) {
	b, ok := <-conn.toUser
	if !ok {
		return 0, ErrConnClosed
	}
	return conn.doReceiveBinary(buf, b)
}

// SelectBinary listens on all given connections until a new message
// arrives, and then reads this message.  If the message received is not
// binary, the channel is closed with status StatusProtocolError and
// [ErrConnClosed] is returned.
//
// If the received message is longer than buf, the buffer contains the start of
// the message and [ErrTooLarge] is returned.  The rest of the message is
// discarded, the connection stays functional.
//
// If the context expires or is cancelled, the error is either
// context.DeadlineExceeded or context.Cancelled.
func SelectBinary(ctx context.Context, buf []byte, clients []*Conn) (idx, n int, err error) {
	idx, b, err := selectChannel(ctx, clients)
	if err != nil {
		return -1, 0, err
	}
	n, err = clients[idx].doReceiveBinary(buf, b)
	return idx, n, err
}

func (conn *Conn) doReceiveBinary(buf []byte, b *reader) (int, error) {
	defer func() { conn.fromUser <- b }()

	if b.header.Opcode != Binary {
		b.connInfo = WrongMessageType
		return 0, ErrConnClosed
	}

	r := &frameReader{rb: b, fromUser: conn.fromUser}
	n, err := r.ReadAll(buf)
	if err != nil && err != ErrTooLarge {
		b.connInfo = ConnDropped
	}
	return n, err
}

// ReceiveText reads a text message from the connection.  If the next received
// message is not a text message, , the channel is closed with status
// StatusProtocolError and [ErrConnClosed] is returned.  If the length of the
// utf-8 representation of the text exceeds maxLength bytes, the text is
// truncated and ErrTooLarge is returned.
func (conn *Conn) ReceiveText(maxLength int) (string, error) {
	b, ok := <-conn.toUser
	if !ok {
		return "", ErrConnClosed
	}
	return conn.doReceiveText(maxLength, b)
}

// SelectText listens on all given connections until a new message arrives, and
// then reads this message.  If the message received is not a text message, the
// channel is closed with status StatusProtocolError and [ErrConnClosed] is
// returned.
//
// If the received text is longer maxLength bytes (in utf-8 encoding), only the
// start of the text together with [ErrTooLarge] is returned.  The rest of the
// text is discarded, the connection stays functional.
//
// If the context expires or is cancelled, the error is either
// context.DeadlineExceeded or context.Cancelled.
func SelectText(ctx context.Context, maxLength int, clients []*Conn) (idx int, text string, err error) {
	idx, b, err := selectChannel(ctx, clients)
	if err != nil {
		return -1, "", err
	}
	text, err = clients[idx].doReceiveText(maxLength, b)
	return idx, text, err
}

func (conn *Conn) doReceiveText(maxLength int, b *reader) (string, error) {
	defer func() { conn.fromUser <- b }()

	if b.header.Opcode != Text {
		b.connInfo = WrongMessageType
		return "", ErrConnClosed
	}

	if b.header.Final && b.header.Length <= int64(maxLength) {
		maxLength = int(b.header.Length)
	}
	buf := make([]byte, maxLength)

	r := &frameReader{rb: b, fromUser: conn.fromUser}
	n, err := r.ReadAll(buf)
	if err != nil && err != ErrTooLarge {
		b.connInfo = ConnDropped
		return "", err
	}

	// check for incomplete/invalid utf-8
	idx := 0
	for idx < n {
		r, size := utf8.DecodeRune(buf[idx:n])
		if r == utf8.RuneError {
			if err == ErrTooLarge && idx > n-utf8.UTFMax && utf8.RuneStart(buf[idx]) {
				// the last rune might be incomplete
				n = idx
				break
			}

			b.connInfo = ProtocolViolation
			return "", ErrConnClosed
		}
		idx += size
	}

	return string(buf[:n]), err
}

func selectChannel(ctx context.Context, clients []*Conn) (int, *reader, error) {
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
			Chan: reflect.ValueOf(conn.toUser),
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
			return -1, nil, ctx.Err()
		}

		if !recvOK {
			// the connection was closed
			numClosed++
			if numClosed == numClients {
				return -1, nil, ErrConnClosed
			}
			cases[idx].Chan = reflect.ValueOf((<-chan *reader)(nil))
			continue
		}

		rb := recv.Interface().(*reader)
		return idx, rb, nil
	}
}
