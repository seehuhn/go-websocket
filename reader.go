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

	if n > 0 && (err == io.EOF || err == io.ErrUnexpectedEOF) {
		err = nil
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
	r := <-conn.getDataReader
	return r.doReceive()
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
	cases := make([]reflect.SelectCase, numClients+1)
	for i, conn := range clients {
		cases[i] = reflect.SelectCase{
			Dir:  reflect.SelectRecv,
			Chan: reflect.ValueOf(conn.getDataReader),
		}
	}
	cases[numClients] = reflect.SelectCase{
		Dir:  reflect.SelectRecv,
		Chan: reflect.ValueOf(ctx.Done()),
	}

	idx, frValue, _ := reflect.Select(cases)
	if idx == numClients {
		return -1, 0, nil, ctx.Err()
	}

	fr := frValue.Interface().(*frameReader)
	tp, r, err := fr.doReceive()
	return idx, tp, r, err
}

func receiveOneLimited(ctx context.Context, want MessageType, buf []byte, clients []*Conn) (idx, n int, err error) {
	idx, tp, r, err := ReceiveOneMessage(ctx, clients)

	if tp == want {
		n, err = io.ReadFull(r, buf)
	} else {
		// we need to discard the message before bailing out
		err = ErrMessageType
	}

	if n > 0 && (err == io.EOF || err == io.ErrUnexpectedEOF) {
		err = nil
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

type frameReader struct {
	Receive <-chan *header
	Result  chan<- struct{}
	Done    chan<- *frameReader

	rw *bufio.ReadWriter

	// the following fields are all initialised by getNextHeader()
	Remaining  uint64
	Mask       [4]byte
	maskPos    int
	confirmed  bool
	Final      bool
	connClosed bool
}

func (r *frameReader) doReceive() (MessageType, io.Reader, error) {
	if r == nil {
		return 0, nil, ErrConnClosed
	}

	opcode := r.getNextHeader()
	if opcode == invalidFrame {
		r.Done <- r
		return 0, nil, ErrConnClosed
	}
	return opcode, r, nil
}

func (r *frameReader) getNextHeader() MessageType {
	header := <-r.Receive
	if header == nil {
		r.confirmed = true
		r.connClosed = true
		return invalidFrame
	}
	r.confirmed = false
	r.Remaining = header.Length
	r.Final = header.Final
	r.Mask = header.Mask
	r.maskPos = 0
	r.connClosed = false
	return header.Opcode
}

// On exit of this function, at least one of the following three
// conditions is true:
//
//   - r.Remaining > 0
//   - r.Final
//   - r.connClosed
//
// The function returns true, if more data is available in the message.
func (r *frameReader) prepareRead() bool {
	for r.Remaining == 0 {
		if !r.confirmed {
			r.Result <- struct{}{}
			r.confirmed = true
		}

		if r.Final || r.connClosed {
			break
		}

		r.getNextHeader()
	}
	return r.Remaining > 0
}

// the function returns true, if reading the message is complete
func (r *frameReader) finishRead() bool {
	if r.Remaining == 0 && !r.confirmed {
		r.Result <- struct{}{}
		r.confirmed = true
	}

	if r.Remaining > 0 || !(r.Final || r.connClosed) {
		return false
	}

	// End of message reached, return a new frameReader to the channel
	// and disable the current frameReader.
	if r.Receive != nil {
		newR := &frameReader{
			Receive: r.Receive,
			Result:  r.Result,
			Done:    r.Done,
			rw:      r.rw,
		}
		r.Done <- newR

		r.Receive = nil
		r.Result = nil
		r.Done = nil
		r.rw = nil
	}
	return true
}

func (r *frameReader) Read(buf []byte) (n int, err error) {
	if r.prepareRead() {
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
	if r.finishRead() && err == nil {
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
		if r.prepareRead() {
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
		if r.finishRead() {
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

	// read the masking key
	_, err = io.ReadFull(conn.rw, buf[:4])
	if err != nil {
		return nil, err
	}

	h := &header{
		Final:  final != 0,
		Opcode: MessageType(opcode),
		Length: length,
	}
	copy(h.Mask[:], buf[:4])

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
func (conn *Conn) readMultiplexer(ready chan<- struct{}) {
	readerDone := make(chan struct{})
	conn.readerDone = readerDone
	drChan := make(chan *frameReader, 1)
	conn.getDataReader = drChan

	close(ready)

	dfChan := make(chan *header, 1)
	resChan := make(chan struct{}, 1)
	r := &frameReader{
		Receive: dfChan,
		Result:  resChan,
		Done:    drChan,
		rw:      conn.rw,
	}
	drChan <- r

	status := StatusInternalServerError
	closeMessage := ""
	needsCont := false
	var headerBuf [10]byte
readLoop:
	for {
		header, err := conn.readFrameHeader(headerBuf[:])
		if err != nil {
			if err == errFrameFormat {
				status = StatusProtocolError
			}
			break readLoop
		}

		switch header.Opcode {
		case Text, Binary, contFrame:
			if (header.Opcode == contFrame) != needsCont {
				status = StatusProtocolError
				break readLoop
			}
			needsCont = !header.Final

			dfChan <- header
			<-resChan
		case closeFrame:
			buf, _ := conn.readFrameBody(header)
			// Since we are exiting anyway, we don't need to check for
			// read errors here.
			var s2 Status
			if len(buf) >= 2 {
				status = 256*Status(buf[0]) + Status(buf[1])
				if status.isValid() && status != StatusNotSent && utf8.Valid(buf[2:]) {
					closeMessage = string(buf[2:])
				} else {
					status = StatusProtocolError
					s2 = StatusNotSent
				}
			} else {
				status = StatusNotSent
			}
			if s2 == 0 {
				s2 = status
			}
			conn.closeMutex.Lock()
			conn.clientStatus = s2
			conn.clientMessage = closeMessage
			conn.closeMutex.Unlock()
			break readLoop
		case pingFrame:
			buf, err := conn.readFrameBody(header)
			if err != nil {
				closeMessage = "read failed"
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
				closeMessage = "read failed"
				break readLoop
			}
		default:
			status = StatusProtocolError
			closeMessage = "invalid opcode"
			break readLoop
		}
	}

	// disable further receives
	close(dfChan)

	// Sending a close response unconditionally is safe,
	// since the server will ignore everything after it has
	// sent a close message once.
	//
	// Since we are exiting anyway, we don't care about errors here.
	_ = conn.sendCloseFrame(status, []byte(closeMessage))

	close(readerDone)
}
