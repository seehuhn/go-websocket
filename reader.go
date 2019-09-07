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
	"bytes"
	"fmt"
	"io"
	"io/ioutil"
	"unicode/utf8"
)

// ReceiveBinary reads a binary message from the connection.  If the
// next received message is not binary, ErrMessageType is returned.
// If the length of the message body exceeds maxLength, the data is
// truncated and ErrTooLarge is returned.
func (conn *Conn) ReceiveBinary(maxLength int) ([]byte, error) {
	buf, err := conn.receiveLimited(Binary, maxLength)
	return buf, err
}

// ReceiveText reads a text message from the connection.  If the next
// received message is not a text message, ErrMessageType is returned.
// If the length of the utf-8 representation of the text exceeds
// maxLength, the text is truncated and ErrTooLarge is returned.
func (conn *Conn) ReceiveText(maxLength int) (string, error) {
	buf, err := conn.receiveLimited(Text, maxLength)
	return string(buf), err
}

func (conn *Conn) receiveLimited(ex MessageType, maxLength int) ([]byte, error) {
	tp, r, err := conn.ReceiveMessage()
	if err != nil {
		return nil, err
	}

	res := &bytes.Buffer{}
	if tp == ex || tp == contFrame {
		_, err = io.CopyN(res, r, int64(maxLength))
	} else {
		err = ErrMessageType
	}

	n, e2 := io.Copy(ioutil.Discard, r)
	if err == nil {
		if n > 0 {
			err = ErrTooLarge
		} else {
			err = e2
		}
	} else if err == io.EOF {
		err = nil
	}

	return res.Bytes(), err
}

// ReceiveMessage returns an io.Reader which can be used to read the
// next message from the connection.  The first return value gives the
// message type received (Text or Binary).
func (conn *Conn) ReceiveMessage() (MessageType, io.Reader, error) {
	r := <-conn.getDataReader
	if r == nil {
		return 0, nil, ErrConnClosed
	}

	header := <-r.Receive
	if header == nil {
		return 0, nil, ErrConnClosed
	}
	r.Pos = 0
	r.Length = header.Length
	r.Final = header.Final
	r.Mask = header.Mask

	if r.Length == 0 {
		r.Result <- struct{}{}
	}

	return header.Opcode, r, nil
}

type frameReader struct {
	Receive <-chan *header
	Result  chan<- struct{}
	Done    chan<- *frameReader

	rw *bufio.ReadWriter

	Pos, Length uint64
	Final       bool
	Mask        []byte
}

func (r *frameReader) Read(buf []byte) (n int, err error) {
	if r.Pos == r.Length && !r.Final && r.Receive != nil {
		// get a new header
	retry:
		header := <-r.Receive
		if header == nil {
			r.Final = true
		} else {
			r.Pos = 0
			r.Length = header.Length
			r.Final = header.Final
			r.Mask = header.Mask
		}

		if r.Length == 0 && !r.Final {
			r.Result <- struct{}{}
			goto retry
		}
	}

	// read data from the network
	maxLen := r.Length - r.Pos
	if maxLen > 0 {
		bufLen64 := uint64(len(buf))
		if maxLen > bufLen64 {
			maxLen = bufLen64
		}

		n, err = r.rw.Read(buf[:maxLen])
		offs := int(r.Pos % 4)
		for i := 0; i < n; i++ {
			buf[i] ^= r.Mask[(i+offs)%4]
		}
		r.Pos += uint64(n)
		if r.Pos == r.Length {
			// tell the multiplexer that we have read our allocated share of bytes
			r.Result <- struct{}{}
		}
	}

	if r.Pos == r.Length && r.Final && r.Receive != nil {
		// End of message reached, return a frameReader to the channel
		// and disable the current frameReader.
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

	if r.Pos == r.Length && r.Receive == nil && err == nil {
		err = io.EOF
	}

	return
}

// readFrameHeader reads and decodes a frame header
func (conn *Conn) readFrameHeader(buf []byte) (*header, error) {
	n, err := io.ReadFull(conn.rw, buf[:2])
	if err != nil {
		if *debug {
			fmt.Printf("|RX error, header [% 02x]: %s\n", buf[:n], err)
		}
		return nil, err
	}

	final := buf[0] & 128
	reserved := (buf[0] >> 4) & 7
	if reserved != 0 {
		if *debug {
			fmt.Printf("|RX error, header [% 02x]: reserved bits set\n", buf[:2])
		}
		return nil, errFrameFormat
	}
	opcode := buf[0] & 15

	mask := buf[1] & 128
	if mask == 0 {
		if *debug {
			fmt.Printf("|RX error, header [% 02x]: missing mask\n", buf[:2])
		}
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
			if *debug {
				fmt.Printf("|RX FRAME: OPCODE=%d FIN=%t, LENGTH ERROR\n",
					opcode, final != 0)
			}
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
		if *debug {
			fmt.Printf("|RX FRAME: OPCODE=%d FIN=%t LEN=%d, MSB ERROR\n",
				opcode, final != 0, length)
		}
		return nil, errFrameFormat
	}

	if opcode >= 8 && (final == 0 || length > 125) {
		if *debug {
			fmt.Printf("|RX FRAME: OPCODE=%d FIN=%t LEN=%d, FRAME ERROR\n",
				opcode, final != 0, length)
		}
		return nil, errFrameFormat
	}

	// read the masking key
	n, _ = io.ReadFull(conn.rw, buf[:4])
	if n < 4 {
		if *debug {
			fmt.Printf("|RX FRAME: OPCODE=%d FIN=%t LEN=%d, MASK ERROR\n",
				opcode, final != 0, length)
		}
		return nil, errFrameFormat
	}

	if *debug {
		fmt.Printf("|RX FRAME: OPCODE=%d FIN=%t LEN=%d\n",
			opcode, final != 0, length)
	}
	return &header{
		Final:  final != 0,
		Opcode: MessageType(opcode),
		Length: length,
		Mask:   buf[:4],
	}, nil
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

	dfChan := make(chan *header)
	resChan := make(chan struct{})
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
			// we are exiting anyway, so we don't need to check for errors here
			if len(buf) >= 2 {
				status = 256*Status(buf[0]) + Status(buf[1])
				if isValidStatus(status) && utf8.Valid(buf[2:]) {
					closeMessage = string(buf[2:])
				} else {
					status = StatusProtocolError
				}
			} else {
				status = statusMissing
				closeMessage = ""
			}
			break readLoop
		case pingFrame:
			buf, err := conn.readFrameBody(header)
			if err != nil {
				closeMessage = "read failed"
				break readLoop
			}
			conn.sendControlFrame <- &frame{
				Opcode: pongFrame,
				Body:   buf,
				Final:  true,
			}
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
	conn.sendCloseFrame(status, []byte(closeMessage))

	close(readerDone)
}
