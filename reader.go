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

type readMultiplexerData struct {
	fromUser <-chan *readBompel
	toUser   chan<- *readBompel

	readerDone chan<- struct{}
}

type readBompel struct {
	r                *bufio.Reader
	sendControlFrame chan<- *frame
	scratch          []byte // buffer for headers and control frame payloads
	header           header
	pos              int64
	failed           bool
}

// ReadMultiplexer reads all data from the network connection.
// Control frames are handled internally by this function,
// data frames are read on behalf of the user.
func (conn *Conn) readMultiplexer(data *readMultiplexerData) {
	b := &readBompel{
		r:                conn.rw.Reader,
		sendControlFrame: conn.sendControlFrame,
		scratch:          make([]byte, 128),
	}

	for {
		b.refill(false)
		if b.failed || b.header.Opcode == closeFrame {
			break
		}
		data.toUser <- b

		b = <-data.fromUser
		if b.failed {
			break
		}
	}

	// Notify the user that no more data will be incoming.
	close(data.toUser)

	// Determine the client status code and message.
	clientStatus := StatusDropped
	var clientMessage string
	if b.header.Opcode == closeFrame && !b.failed {
		body := b.scratch[:b.header.Length]
		if len(body) >= 2 {
			clientStatus = 256*Status(body[0]) + Status(body[1])
			clientMessage = string(body[2:])
		} else {
			clientStatus = StatusNotSent
		}
	}
	conn.closeMutex.Lock()
	conn.clientStatus = clientStatus
	conn.clientMessage = clientMessage
	conn.closeMutex.Unlock()

	// We may already have sent a close frame in the [Conn.Close] method.
	// Sending a close frame here unconditionally is safe, since the client
	// will ignore everything after it has received the first close message.
	//
	// Since we are exiting anyway, we don't care about errors here.
	var closeStatus Status
	if clientStatus.serverCanSend() {
		closeStatus = clientStatus
	} else {
		closeStatus = StatusProtocolError
	}
	conn.sendCloseFrame(closeStatus, nil)

	// Closing this will terminate the writer, so this must come after the call
	// to [Conn.sendCloseFrame].
	close(data.readerDone)
}

func (b *readBompel) refill(isCont bool) error {
	for {
		err := b.readFrameHeader()
		if err != nil {
			b.failed = true
			return err
		}

		if b.header.Opcode >= 8 { // control frame
			if b.header.Length > 125 {
				// All control frames MUST have a payload length of 125 bytes or less
				// and MUST NOT be fragmented.
				b.failed = true
				return ErrConnClosed
			}
			_, err = io.ReadFull(b.r, b.scratch[:b.header.Length])
			if err != nil {
				b.failed = true
				return err
			}
			b.unmask(b.scratch[:b.header.Length])
		}

		switch b.header.Opcode {
		case Text, Binary:
			if isCont {
				b.failed = true
				return ErrConnClosed
			}
			return nil

		case contFrame:
			if !isCont {
				b.failed = true
				return ErrConnClosed
			}
			return nil

		case closeFrame:
			if b.header.Length >= 2 {
				recvStatus := 256*Status(b.scratch[0]) + Status(b.scratch[1])
				if !recvStatus.clientCanSend() || !utf8.Valid(b.scratch[2:b.header.Length]) {
					b.failed = true
				}
			} else if b.header.Length == 1 {
				b.failed = true
			}
			return ErrConnClosed

		case pingFrame:
			body := make([]byte, b.header.Length)
			copy(body, b.scratch[:b.header.Length])

			ctl := framePool.Get().(*frame)
			ctl.Opcode = pongFrame
			ctl.Body = body
			ctl.Final = true
			b.sendControlFrame <- ctl

		case pongFrame:
			// we ignore pong frames

		default:
			b.failed = true
			return ErrConnClosed
		}
	}
}

func (b *readBompel) readFrameHeader() error {
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

func (b *readBompel) unmask(buf []byte) {
	for i := range buf {
		buf[i] ^= b.header.Mask[b.pos&3]
		b.pos++
	}
}

type frameReader struct {
	b        *readBompel
	fromUser chan<- *readBompel
}

func (fr *frameReader) Read(buf []byte) (int, error) {
	b := fr.b
	for b.pos >= b.header.Length && !b.header.Final {
		err := b.refill(true)
		if err != nil {
			return 0, err
		}
	}
	// now there is either data available, or b.final is set (or both)

	amount := len(buf)
	if int64(amount) > b.header.Length-b.pos {
		amount = int(b.header.Length - b.pos)
	}
	n, err := b.r.Read(buf[:amount])
	b.unmask(buf[:n])
	if err != nil {
		b.failed = true
		return n, err
	}

	if b.pos >= b.header.Length && b.header.Final {
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
		fr.fromUser <- fr.b
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

	fr := &frameReader{b: b, fromUser: conn.fromUser}
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

	fr := &frameReader{b: b, fromUser: clients[idx].fromUser}
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

// ReceiveOneBinary listens on all given connections until a new message
// arrives, and then reads this message.  If the message received is not
// binary, the channel is closed with status StatusProtocolError and
// [ErrConnClosed] is returned.
//
// If the received message is longer than buf, the buffer contains the start of
// the message and [ErrTooLarge] is returned.
//
// If the context expires or is cancelled, the error is either
// context.DeadlineExceeded or context.Cancelled.
func ReceiveOneBinary(ctx context.Context, buf []byte, clients []*Conn) (idx, n int, err error) {
	idx, b, err := selectChannel(ctx, clients)
	if err != nil {
		return -1, 0, err
	}
	n, err = clients[idx].doReceiveBinary(buf, b)
	return idx, n, err
}

func (conn *Conn) doReceiveBinary(buf []byte, b *readBompel) (int, error) {
	defer func() { conn.fromUser <- b }()

	if b.header.Opcode != Binary {
		b.failed = true
		return 0, ErrConnClosed
	}

	r := &frameReader{b: b, fromUser: conn.fromUser}
	n, err := r.ReadAll(buf)
	if err != nil && err != ErrTooLarge {
		b.failed = true
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

func ReceiveOneText(ctx context.Context, maxLength int, clients []*Conn) (idx int, text string, err error) {
	idx, b, err := selectChannel(ctx, clients)
	if err != nil {
		return -1, "", err
	}
	text, err = clients[idx].doReceiveText(maxLength, b)
	return idx, text, err
}

func (conn *Conn) doReceiveText(maxLength int, b *readBompel) (string, error) {
	defer func() { conn.fromUser <- b }()

	if b.header.Opcode != Text {
		b.failed = true
		return "", ErrConnClosed
	}

	if b.header.Final && b.header.Length <= int64(maxLength) {
		maxLength = int(b.header.Length)
	}
	buf := make([]byte, maxLength)

	r := &frameReader{b: b, fromUser: conn.fromUser}
	n, err := r.ReadAll(buf)
	if err != nil && err != ErrTooLarge {
		b.failed = true
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

			b.failed = true
			return "", ErrConnClosed
		}
		idx += size
	}

	return string(buf[:n]), err
}

func selectChannel(ctx context.Context, clients []*Conn) (int, *readBompel, error) {
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
			cases[idx].Chan = reflect.ValueOf((<-chan *readBompel)(nil))
			continue
		}

		b := recv.Interface().(*readBompel)
		return idx, b, nil
	}
}
