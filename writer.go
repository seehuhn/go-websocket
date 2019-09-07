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
	"io"
)

const maxHeaderSize = 10

// SendText sends a text message to the client.
func (conn *Conn) SendText(msg string) error {
	return conn.sendData(Text, []byte(msg))
}

// SendBinary sends a binary message to the client.
func (conn *Conn) SendBinary(msg []byte) error {
	return conn.sendData(Binary, msg)
}

func (conn *Conn) sendData(opcode MessageType, data []byte) error {
	// Get the frameWriter just to reserve the data channel, but we
	// just send the data manually in one frame, rather than using the
	// Write() method.
	w := <-conn.getDataWriter
	if w == nil {
		return ErrConnClosed
	}

	msg := &frame{
		Opcode: opcode,
		Body:   data,
		Final:  true,
	}
	w.Send <- msg

	err := <-w.Result
	w.Done <- w
	return err
}

// SendMessage starts a new message and returns an io.WriteCloser
// which can be used to send the message body.  The argument tp gives
// the message type (Text or Binary).  Text messages must be sent in
// utf-8 encoded form.
func (conn *Conn) SendMessage(tp MessageType) (io.WriteCloser, error) {
	if tp != Text && tp != Binary {
		return nil, ErrMessageType
	}
	w := <-conn.getDataWriter
	if w == nil {
		return nil, ErrConnClosed
	}

	w.Pos = 0
	w.Opcode = tp
	return w, nil
}

type frameWriter struct {
	Buffer []byte
	Send   chan<- *frame
	Result <-chan error
	Done   chan<- *frameWriter
	Pos    int
	Opcode MessageType
}

func (w *frameWriter) Write(buf []byte) (total int, err error) {
	for {
		n := copy(w.Buffer[w.Pos:], buf)
		total += n
		w.Pos += n
		buf = buf[n:]

		if len(buf) == 0 {
			return
		}

		msg := &frame{
			Opcode: w.Opcode,
			Body:   w.Buffer,
			Final:  false,
		}
		w.Send <- msg

		w.Pos = 0
		w.Opcode = contFrame

		err = <-w.Result
		if err != nil {
			return
		}
	}
}

func (w *frameWriter) Close() error {
	msg := &frame{
		Opcode: w.Opcode,
		Body:   w.Buffer[:w.Pos],
		Final:  true,
	}
	w.Send <- msg

	err := <-w.Result
	w.Done <- w // put back the frameWriter for the next user
	return err
}

func (conn *Conn) writeFrame(opcode MessageType, body []byte, final bool) error {
	var header [maxHeaderSize]byte

	header[0] = byte(opcode)
	if final {
		header[0] |= 128
	}

	l := uint64(len(body))
	n := 2
	if l < 126 {
		header[1] = byte(l)
	} else if l < (1 << 16) {
		header[1] = 126
		header[2] = byte(l >> 8)
		header[3] = byte(l)
		n = 4
	} else {
		header[1] = 127
		header[2] = byte(l >> 56)
		header[3] = byte(l >> 48)
		header[4] = byte(l >> 40)
		header[5] = byte(l >> 32)
		header[6] = byte(l >> 24)
		header[7] = byte(l >> 16)
		header[8] = byte(l >> 8)
		header[9] = byte(l)
		n = 10
	}

	_, err := conn.rw.Write(header[:n])
	if err != nil {
		return err
	}
	_, err = conn.rw.Write(body)
	if err != nil {
		return err
	}
	return conn.rw.Flush()
}

// writeFrames multiplexes all output to the network channel.
// Shutdown is initiated by sending a close frame via
// conn.sendControlFrame.  After this, the function drains all
// channels, returning ErrConnClosed for all write attempts, and
// terminates once conn.sendControlFrame is closed.
func (conn *Conn) writeMultiplexer(ready chan<- struct{}) {
	writerDone := make(chan struct{})
	conn.writerDone = writerDone
	cfChan := make(chan *frame, 1)
	conn.sendControlFrame = cfChan
	dwChan := make(chan *frameWriter, 1)
	conn.getDataWriter = dwChan

	close(ready)

	dataBufferSize := conn.rw.Writer.Size() - maxHeaderSize
	if dataBufferSize < 512-maxHeaderSize {
		dataBufferSize = 512 - maxHeaderSize
	}
	dfChan := make(chan *frame)
	resChan := make(chan error)
	w := &frameWriter{
		Buffer: make([]byte, dataBufferSize),
		Send:   dfChan,
		Result: resChan,
		Done:   dwChan,
	}
	dwChan <- w

writerLoop:
	for {
		select {
		case frame := <-dfChan:
			err := conn.writeFrame(frame.Opcode, frame.Body, frame.Final)
			resChan <- err
		case frame := <-cfChan:
			conn.writeFrame(frame.Opcode, frame.Body, true)

			if frame.Opcode == closeFrame {
				break writerLoop
			}
		}
	}

	// from this point onwards we don't write to the connection any more
	close(writerDone)

drainLoop:
	for {
		select {
		case _ = <-dwChan:
			close(dwChan)
			dwChan = nil
		case _ = <-dfChan:
			resChan <- ErrConnClosed
		case _, ok := <-cfChan:
			if !ok {
				break drainLoop
			}
		}
	}
}
