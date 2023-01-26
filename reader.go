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

type messageInfo struct {
	messageType MessageType
	sizeHint    int64 // -1 if unknown
}

type readMultiplexerData struct {
	newMessage chan<- *messageInfo
	fromUser   <-chan []byte
	toUser     chan<- int

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
readLoop:
	for {
		header, err := conn.readFrameHeader(scratch)
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
				sizeHint := int64(-1)
				if header.Final {
					sizeHint = header.Length
				}
				data.newMessage <- &messageInfo{
					messageType: header.Opcode,
					sizeHint:    sizeHint,
				}
			}

			available := header.Length
			done := false
			for !done {
				buf := <-data.fromUser

				amount := len(buf)
				if amount > int(available) {
					amount = int(available)
				}
				n, err := conn.rw.Read(buf[:amount])
				if check(err) {
					break readLoop
				}
				available -= int64(n)

				done := header.Final && available == 0

				if done {
					// Send a negative amount to signal that the
					// message is complete.
					data.toUser <- -n
				} else {
					data.toUser <- n
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
	for range data.fromUser {
		// drain the channel of user requests
	}
}

// readFrameHeader reads and decodes a frame header
func (conn *Conn) readFrameHeader(scratch []byte) (*header, error) {
	_, err := io.ReadFull(conn.rw, scratch[:2])
	if err != nil {
		return nil, err
	}

	final := scratch[0] & 128
	reserved := (scratch[0] >> 4) & 7
	if reserved != 0 {
		return nil, errFrameFormat
	}
	opcode := scratch[0] & 15

	mask := scratch[1] & 128
	if mask == 0 {
		return nil, errFrameFormat
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
			return nil, errFrameFormat
		}
	} else {
		scratch[0] = l8
	}
	var length uint64
	for i := 0; i < lengthBytes; i++ {
		length = length<<8 | uint64(scratch[i])
	}
	if length&(1<<63) != 0 {
		return nil, errFrameFormat
	}

	if opcode >= 8 && (final == 0 || length > 125) {
		return nil, errFrameFormat
	}

	h := &header{
		Final:  final != 0,
		Opcode: MessageType(opcode),
		Length: int64(length),
	}

	// read the masking key
	_, err = io.ReadFull(conn.rw, h.Mask[:])
	if err != nil {
		return nil, err
	}

	return h, nil
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
// arrives. The function returns the index of the connection, the message type,
// and a reader which can be used to read the message contents.
//
// No more messages can be received on this connection until the returned
// io.Reader has been drained.  In order to avoid deadlocks, the reader must
// always read the complete message.
//
// If the context expires or is cancelled, the error is either
// context.DeadlineExceeded or context.Cancelled.
//
// If more than 65535 clients are given, the function panics.
func ReceiveOneMessage(ctx context.Context, clients []*Conn) (int, MessageType, io.Reader, error) {
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
			return -1, 0, nil, ctx.Err()
		}

		if !recvOK {
			// the connection was closed
			numClosed++
			if numClosed == numClients {
				return -1, 0, nil, ErrConnClosed
			}
			cases[idx].Chan = reflect.ValueOf((<-chan *messageInfo)(nil))
			continue
		}

		msgInfo := recv.Interface().(*messageInfo)
		r := &frameReader{conn: clients[idx]}
		return idx, msgInfo.messageType, r, nil
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
	n := <-r.conn.toUser
	if n < 0 {
		r.isEOF = true
		return -n, io.EOF
	}
	return n, nil
}

// ReceiveBinary reads a binary message from the connection.  If the next
// received message is not binary, the channel is closed with status
// StatusProtoclError and [ErrConnClosed] is returned.  If the received message
// is longer than buf, the buffer contains the start of the message and
// [ErrTooLarge] is returned.
func (conn *Conn) ReceiveBinary(buf []byte) (int, error) {
	msgInfo, ok := <-conn.newMessage
	if !ok {
		return 0, ErrConnClosed
	}

	if msgInfo.messageType != Binary {
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
		n := <-conn.toUser
		if n < 0 {
			n = -n
		}
		pos += n
	}

	if !done {
		for {
			conn.fromUser <- nil
			n := <-conn.toUser
			if n < 0 {
				return len(buf), ErrTooLarge
			}
		}
	}

	return pos, nil
}

// =======================================================================

// ReceiveBinaryOld reads a binary message from the connection.  If the
// next received message is not binary, ErrMessageType is returned and
// the received message is discarded.  If the received message is
// longer than buf, buf contains the start of the message and
// ErrTooLarge is returned.
func (conn *Conn) ReceiveBinaryOld(buf []byte) (n int, err error) {
	return conn.receiveLimited(Binary, buf)
}

// ReceiveText reads a text message from the connection.  If the next received
// message is not a text message, ErrMessageType is returned and the received
// message is discarded.  If the length of the utf-8 representation of the text
// exceeds maxLength bytes, the text is truncated and ErrTooLarge is returned.
func (conn *Conn) ReceiveText(maxLength int) (string, error) {
	buf := make([]byte, maxLength)
	n, err := conn.receiveLimited(Text, buf)
	return string(buf[:n]), err
}

func (conn *Conn) receiveLimited(want MessageType, buf []byte) (n int, err error) {
	tp, r, err := conn.ReceiveMessageOld()
	if err != nil {
		return 0, err
	}

	if tp == want {
		n, err = io.ReadFull(r, buf)
	} else {
		// we need to discard the message before bailing out
		err = ErrMessageType
	}

	if err == io.EOF || err == io.ErrUnexpectedEOF {
		if n > 0 {
			err = nil
		}
	} else {
		n2, e2 := r.(*frameReaderOLD).Discard()
		if err == nil && n2 > 0 {
			err = ErrTooLarge
		} else if err == nil {
			err = e2
		}
	}
	return n, err
}

// ReceiveMessageOld returns an io.Reader which can be used to read the next
// message from the connection.  The first return value gives the message type
// received (Text or Binary).
//
// No more messages can be received until the returned io.Reader has been
// darined.  In order to avoid deadlocks, the reader must always read the
// complete message.
func (conn *Conn) ReceiveMessageOld() (MessageType, io.Reader, error) {
	r := <-conn.getFrameReaderOLD
	if r == nil {
		return 0, nil, ErrConnClosed
	}

	header := <-r.GetHeader
	if header == nil {
		r.confirmed = true
		r.connClosed = true
		r.MessageDone <- r
		return 0, nil, ErrConnClosed
	}

	r.processHeader(header)
	return header.Opcode, r, nil
}

// ReceiveOneBinary listens on all connections until a new message arrives,
// and returns this message.  If the next received message is not binary,
// ErrMessageType is returned and the received message is discarded.  If the
// received message is longer than buf, buf contains the start of the message
// and ErrTooLarge is returned.
//
// If the context expires or is cancelled, -1 is returned for the
// connection index, and the error is either context.Cancelled or
// context.DeadlineExceeded.
func ReceiveOneBinary(ctx context.Context, buf []byte, clients []*Conn) (idx, n int, err error) {
	return receiveOneLimited(ctx, Binary, buf, clients)
}

// ReceiveOneText listens on all connections until a new message arrives, and
// returns this message.  If the next received message is not a text message,
// ErrMessageType is returned and the received message is discarded.  If the
// length of the utf-8 representation of the text exceeds maxLength bytes, the
// text is truncated and ErrTooLarge is returned.
//
// If the context expires or is cancelled, -1 is returned for the
// connection index, and the error is either context.Cancelled or
// context.DeadlineExceeded.
func ReceiveOneText(ctx context.Context, maxLength int, clients []*Conn) (int, string, error) {
	buf := make([]byte, maxLength)
	idx, n, err := receiveOneLimited(ctx, Text, buf, clients)
	return idx, string(buf[:n]), err
}

func receiveOneLimited(ctx context.Context, want MessageType, buf []byte, clients []*Conn) (idx, n int, err error) {
	idx, tp, r, err := ReceiveOneMessage(ctx, clients)
	if err != nil {
		return idx, 0, err
	}

	if tp == want {
		n, err = io.ReadFull(r, buf)
	} else {
		// we need to discard the message before bailing out
		err = ErrMessageType
	}

	if err == io.EOF || err == io.ErrUnexpectedEOF {
		if n > 0 {
			err = nil
		}
	} else {
		n2, e2 := r.(*frameReaderOLD).Discard()
		if err == nil && n2 > 0 {
			err = ErrTooLarge
		} else if err == nil {
			err = e2
		}
	}
	return idx, n, err
}

type frameReaderOLD struct {
	// GetHeader synchronises access to `rw` between readers. A client is only
	// allowed to read from `.rw`, after receiving a header via this channel.
	// Once a header has been received, the frame body must be read by the
	// client.  When reading is complete, a write to `.FrameDone` must be
	// used to return control over `.rw` to the server.
	GetHeader <-chan *header

	// Once a client has finished reading the body of a frame, a send to
	// `.FrameDone` must be used to return control over `.rw` to the server.
	FrameDone chan<- struct{}

	// MessageDone is used by clients to return the frameReader once a message
	// has been read completely.
	MessageDone chan<- *frameReaderOLD

	rw *bufio.ReadWriter

	// the following fields are all initialised by processHeader()
	Remaining  int64
	Mask       [4]byte
	maskPos    int
	confirmed  bool
	Final      bool
	connClosed bool
}

func (r *frameReaderOLD) processHeader(header *header) {
	r.confirmed = false
	r.Remaining = header.Length
	r.Final = header.Final
	r.Mask = header.Mask
	r.maskPos = 0
	r.connClosed = false
}

// On exit of this function, at least one of the following three
// conditions is true:
//
//   - r.Remaining > 0 (returns true in this case only)
//   - r.Final
//   - r.connClosed
//
// The function returns true, if more data is available in the message.
func (r *frameReaderOLD) hasAvailableData() bool {
	for r.Remaining == 0 {
		if !r.confirmed {
			r.FrameDone <- struct{}{}
			r.confirmed = true
		}

		if r.Final || r.connClosed {
			break
		}

		header := <-r.GetHeader
		if header == nil {
			r.confirmed = true
			r.connClosed = true
		} else {
			r.processHeader(header)
		}
	}
	return r.Remaining > 0
}

// the function returns true, if reading the message is complete
func (r *frameReaderOLD) isDone() bool {
	if r.Remaining == 0 && !r.confirmed {
		r.FrameDone <- struct{}{}
		r.confirmed = true
	}

	if r.Remaining > 0 || !(r.Final || r.connClosed) {
		return false
	}

	// End of message reached, return a new frameReader to the channel
	// and disable the current frameReader.
	if r.GetHeader != nil {
		newR := &frameReaderOLD{
			GetHeader:   r.GetHeader,
			FrameDone:   r.FrameDone,
			MessageDone: r.MessageDone,
			rw:          r.rw,
		}
		r.MessageDone <- newR

		r.GetHeader = nil
		r.FrameDone = nil
		r.MessageDone = nil
		r.rw = nil
	}
	return true
}

func (r *frameReaderOLD) Read(buf []byte) (n int, err error) {
	if r.hasAvailableData() {
		// read data from the network
		maxLen := r.Remaining
		bufLen64 := int64(len(buf))
		if maxLen > bufLen64 {
			maxLen = bufLen64
		}

		n, err = r.rw.Read(buf[:maxLen])
		r.Remaining -= int64(n)

		offs := r.maskPos
		for i := 0; i < n; i++ {
			buf[i] ^= r.Mask[(i+offs)%4]
		}
		r.maskPos = (offs + n) % 4
	}
	if r.isDone() && err == nil {
		if r.Final {
			err = io.EOF
		} else if r.connClosed {
			err = ErrConnClosed
		}
	}
	return
}

func (r *frameReaderOLD) Discard() (int64, error) {
	var total int64

	// Discard in blocks, in case the remaining length does not fit
	// into an int.
	var blockSize int64 = 1024 * 1024 * 1024 // 1GB

	for {
		if r.hasAvailableData() {
			r64 := r.Remaining
			if r64 > blockSize {
				r64 = blockSize
			}
			n, err := r.rw.Discard(int(r64))
			n64 := int64(n)
			r.Remaining -= n64
			total += n64
			if err != nil {
				return total, err
			}
		}
		if r.isDone() {
			break
		}
	}

	return total, nil
}
