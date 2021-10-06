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

// ReceiveBinary reads a binary message from the connection.  If the
// next received message is not binary, ErrMessageType is returned and
// the received message is discarded.  If the received message is
// longer than buf, buf contains the start of the message and
// ErrTooLarge is returned.
func (conn *Conn) ReceiveBinary(buf []byte) (n int, err error) {
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
	tp, r, err := conn.ReceiveMessage()
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
		n2, e2 := r.(*frameReader).Discard()
		if err == nil && n2 > 0 {
			err = ErrTooLarge
		} else if err == nil {
			err = e2
		}
	}
	return n, err
}

// ReceiveMessage returns an io.Reader which can be used to read the
// next message from the connection.  The first return value gives the
// message type received (Text or Binary).
//
// No more messages can be received until the returned io.Reader has
// been read till the end.  In order to avoid deadlocks, the reader
// must always be completely drained.
func (conn *Conn) ReceiveMessage() (MessageType, io.Reader, error) {
	r := <-conn.getFrameReader
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
		n2, e2 := r.(*frameReader).Discard()
		if err == nil && n2 > 0 {
			err = ErrTooLarge
		} else if err == nil {
			err = e2
		}
	}
	return idx, n, err
}

// ReceiveOneMessage listens on all connections until a new message arrives.
// The function returns the index of the connection, the message type, and a
// reader which can be used to read the message contents.
//
// No more messages can be received on this connection until the returned
// io.Reader has been read till the end.  In order to avoid deadlocks, the
// reader must always be completely drained.
//
// If the context expires or is cancelled, -1 is returned for the
// connection index, and the error is either context.Cancelled or
// context.DeadlineExceeded.
func ReceiveOneMessage(ctx context.Context, clients []*Conn) (int, MessageType, io.Reader, error) {
	numClients := len(clients)

	// Set up channels to wait on in the select statement.
	// Initially we wait only for readers and for the context to be cancelled.
	// Once we have readers, we switch to listening for messages instead.
	cases := make([]reflect.SelectCase, numClients, numClients+1)
	readers := make([]*frameReader, numClients)
	for i, conn := range clients {
		cases[i] = reflect.SelectCase{
			Dir:  reflect.SelectRecv,
			Chan: reflect.ValueOf(conn.getFrameReader),
		}
	}
	done := ctx.Done()
	if done != nil {
		cases = append(cases, reflect.SelectCase{
			Dir:  reflect.SelectRecv,
			Chan: reflect.ValueOf(done),
		})
	} else {
		cases = cases[:numClients]
	}

	// Listen until we receive the first message on a reader, or until
	// the context is cancelled.
	var idxSelected int
	var idxError error
	var opcode MessageType
	for {
		idx, recv, recvOK := reflect.Select(cases)
		idxSelected = idx

		if idx == numClients {
			// the context was cancelled
			break
		}

		if !recvOK {
			idxError = ErrConnClosed
			break
		}

		if readers[idx] == nil {
			// We received a new reader.

			fr := recv.Interface().(*frameReader)
			if fr == nil { // TODO(voss): can this happen?
				idxError = ErrConnClosed
				break
			}

			// Switch to listening for the first packet on this reader.
			readers[idx] = fr
			cases[idx].Chan = reflect.ValueOf(fr.GetHeader)
		} else {
			// We received data on a reader.

			r := readers[idx]
			header := recv.Interface().(*header)
			if header == nil {
				r.confirmed = true
				r.connClosed = true
				r.MessageDone <- r
				idxError = ErrConnClosed
				break
			}
			r.processHeader(header)
			opcode = header.Opcode
			break
		}
	}

	// Release all the readers except for the selected one.
	for i, r := range readers {
		if r == nil || i == idxSelected {
			continue
		}
		r.MessageDone <- r
	}

	if idxSelected == numClients {
		return -1, 0, nil, ctx.Err()
	}
	if idxError != nil {
		return idxSelected, 0, nil, idxError
	}
	return idxSelected, opcode, readers[idxSelected], nil
}

type frameReader struct {
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
	MessageDone chan<- *frameReader

	rw *bufio.ReadWriter

	// the following fields are all initialised by processHeader()
	Remaining  uint64
	Mask       [4]byte
	maskPos    int
	confirmed  bool
	Final      bool
	connClosed bool
}

func (r *frameReader) processHeader(header *header) {
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
func (r *frameReader) hasAvailableData() bool {
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
func (r *frameReader) isDone() bool {
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
		newR := &frameReader{
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

func (r *frameReader) Read(buf []byte) (n int, err error) {
	if r.hasAvailableData() {
		// read data from the network
		maxLen := r.Remaining
		bufLen64 := uint64(len(buf))
		if maxLen > bufLen64 {
			maxLen = bufLen64
		}

		n, err = r.rw.Read(buf[:maxLen])
		r.Remaining -= uint64(n)

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

func (r *frameReader) Discard() (uint64, error) {
	var total uint64

	// Discard in blocks, in case the remaining length does not fit
	// into an int.
	var blockSize uint64 = 1024 * 1024 * 1024 // 1GB

	for {
		if r.hasAvailableData() {
			r64 := r.Remaining
			if r64 > blockSize {
				r64 = blockSize
			}
			n, err := r.rw.Discard(int(r64))
			n64 := uint64(n)
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

// readFrameHeader reads and decodes a frame header
func (conn *Conn) readFrameHeader(buf []byte) (*header, error) {
	_, err := io.ReadFull(conn.rw, buf[:2])
	if err != nil {
		return nil, err
	}

	final := buf[0] & 128
	reserved := (buf[0] >> 4) & 7
	if reserved != 0 {
		return nil, errFrameFormat
	}
	opcode := buf[0] & 15

	mask := buf[1] & 128
	if mask == 0 {
		return nil, errFrameFormat
	}

	// read the length
	l8 := buf[1] & 127
	lengthBytes := 1
	if l8 == 127 {
		lengthBytes = 8
	} else if l8 == 126 {
		lengthBytes = 2
	}
	if lengthBytes > 1 {
		n, _ := io.ReadFull(conn.rw, buf[:lengthBytes])
		if n < lengthBytes {
			return nil, errFrameFormat
		}
	} else {
		buf[0] = l8
	}
	var length uint64
	for i := 0; i < lengthBytes; i++ {
		length = length<<8 | uint64(buf[i])
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
		Length: length,
	}

	// read the masking key
	_, err = io.ReadFull(conn.rw, h.Mask[:])
	if err != nil {
		return nil, err
	}

	return h, nil
}

func (conn *Conn) readFrameBody(header *header) ([]byte, error) {
	body := make([]byte, header.Length)
	n, err := io.ReadFull(conn.rw, body)
	for i := 0; i < n; i++ {
		body[i] ^= header.Mask[i%4]
	}
	return body[:n], err
}

// readMultiplexer multiplexes all data reads from the network.
// Control frames are handled internally by this function.
func (conn *Conn) readMultiplexer(isFunctional chan<- struct{}) {
	readerDone := make(chan struct{})
	conn.readerDone = readerDone
	frChan := make(chan *frameReader, 1)
	conn.getFrameReader = frChan

	close(isFunctional)

	headerChan := make(chan *header, 1)
	frameDone := make(chan struct{}, 1)
	r := &frameReader{
		GetHeader:   headerChan,
		FrameDone:   frameDone,
		MessageDone: frChan,
		rw:          conn.rw,
	}
	frChan <- r

	// data for the close frame we will send to the client at the end
	sendStatus := StatusInternalServerError

	needsCont := false
	var headerBuf [8]byte
readLoop:
	for {
		header, err := conn.readFrameHeader(headerBuf[:])
		if err != nil {
			if err == errFrameFormat {
				sendStatus = StatusProtocolError
			}
			break readLoop
		}

		switch header.Opcode {
		case Text, Binary, contFrame:
			if (header.Opcode == contFrame) != needsCont {
				sendStatus = StatusProtocolError
				break readLoop
			}
			needsCont = !header.Final

			headerChan <- header
			<-frameDone // wait for the client to read the body of the frame
		case closeFrame:
			// Since we are exiting anyway, we don't need to check for
			// read errors here:
			buf, _ := conn.readFrameBody(header)

			sendStatus = StatusOK
			recvStatus := StatusNotSent
			var recvMessage string
			if len(buf) >= 2 {
				recvStatus = 256*Status(buf[0]) + Status(buf[1])
				if recvStatus.isValid() && recvStatus != StatusNotSent && utf8.Valid(buf[2:]) {
					sendStatus = recvStatus
					recvMessage = string(buf[2:])
				} else {
					sendStatus = StatusProtocolError
				}
			} else if len(buf) == 1 {
				recvStatus = StatusDropped
				sendStatus = StatusProtocolError
			}
			conn.closeMutex.Lock()
			conn.clientStatus = recvStatus
			conn.clientMessage = recvMessage
			conn.closeMutex.Unlock()
			break readLoop
		case pingFrame:
			buf, err := conn.readFrameBody(header)
			if err != nil {
				break readLoop
			}
			ctl := framePool.Get().(*frame)
			ctl.Opcode = pongFrame
			ctl.Body = buf
			ctl.Final = true
			conn.sendControlFrame <- ctl
		case pongFrame:
			// we don't send ping frames, so we just swallow pong frames
			_, err := conn.readFrameBody(header)
			if err != nil {
				break readLoop
			}
		default:
			sendStatus = StatusProtocolError
			break readLoop
		}
	}

	// disable further receives
	close(headerChan)

	// Sending a close response unconditionally is safe,
	// since the server will ignore everything after it has
	// sent a close message once.
	//
	// Since we are exiting anyway, we don't care about errors here.
	_ = conn.sendCloseFrame(sendStatus, nil)

	close(readerDone)
}
