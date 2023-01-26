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
	"reflect"
)

const maxHeaderSize = 10

// SendText sends a text message to the client.
func (conn *Conn) SendText(msg string) error {
	return conn.sendData(Text, []byte(msg))
}

// SendBinary sends a binary message to the client.
//
// For streaming large messages, use SendMessage() instead.
func (conn *Conn) SendBinary(msg []byte) error {
	return conn.sendData(Binary, msg)
}

func (conn *Conn) sendData(opcode MessageType, data []byte) error {
	// Get the frameWriter just to reserve the data channel, but we
	// just send the data manually in one frame, rather than using the
	// Write() method.
	w := <-conn.getFrameWriter
	if w == nil {
		return ErrConnClosed
	}

	msg := framePool.Get().(*frame)
	msg.Opcode = opcode
	msg.Body = data
	msg.Final = true
	w.Send <- msg

	err := <-w.Result
	w.Done <- w
	return err
}

// BroadcastText sends a text message to all of the given clients.
func BroadcastText(ctx context.Context, msg string, clients []*Conn) map[int]error {
	return broadcastData(ctx, Text, []byte(msg), clients)
}

// BroadcastBinary sends a binary message to all of the given clients.
func BroadcastBinary(ctx context.Context, data []byte, clients []*Conn) map[int]error {
	return broadcastData(ctx, Binary, data, clients)
}

func broadcastData(ctx context.Context, opcode MessageType, data []byte, clients []*Conn) map[int]error {
	numClients := len(clients)
	cases := make([]reflect.SelectCase, numClients, numClients+1)
	writers := make([]*frameWriter, numClients)
	errors := make(map[int]error)

	disabled := reflect.Zero(reflect.ChanOf(reflect.BothDir,
		reflect.TypeOf(&frameReader{})))

	// each client goes through the following stages:
	//   * w := <-conn.getFrameWriter (abort with error if nil)
	//     - We are here while writers[i] == nil
	//
	//   * w.Send <- data
	//     - We are here while writers[i] != nil && cases[i].Chan != disabled
	//
	//   * err := <-w.Result
	//     - We are here while writers[i] != nil && cases[i].Chan != disabled
	//
	//   * w.Done <- w (Done channel is buffered, cannot hang)
	//     - We are here if cases[i].Chan == disabled

	for i, conn := range clients {
		cases[i] = reflect.SelectCase{
			Dir:  reflect.SelectRecv,
			Chan: reflect.ValueOf(conn.getFrameWriter),
		}
	}
	done := ctx.Done()
	if done != nil {
		cases = append(cases, reflect.SelectCase{
			Dir:  reflect.SelectRecv,
			Chan: reflect.ValueOf(done),
		})
	}

	todo := numClients
mainLoop:
	for todo > 0 {
		idx, recv, recvOK := reflect.Select(cases)

		switch {
		case idx == numClients:
			// the context was cancelled
			err := ctx.Err()
			for i := 0; i < numClients; i++ {
				if cases[i].Chan != disabled {
					errors[i] = err
				}
			}
			break mainLoop
		case writers[idx] == nil:
			if !recvOK {
				errors[idx] = ErrConnClosed
				cases[idx].Chan = disabled
				todo--
				continue mainLoop
			}
			// w := <-conn.getFrameWriter has succeeded,
			// send the data next
			w := recv.Interface().(*frameWriter)
			writers[idx] = w

			msg := framePool.Get().(*frame)
			msg.Opcode = opcode
			msg.Body = data
			msg.Final = true

			cases[idx].Dir = reflect.SelectSend
			cases[idx].Chan = reflect.ValueOf(w.Send)
			cases[idx].Send = reflect.ValueOf(msg)
		case cases[idx].Dir == reflect.SelectSend:
			// w.Send <- data has succeeded,
			// read the error next
			cases[idx].Dir = reflect.SelectRecv
			cases[idx].Chan = reflect.ValueOf(writers[idx].Result)
			cases[idx].Send = reflect.Value{}
		default:
			// err := <-w.Result has succeded,
			// after this we are done
			w := writers[idx]
			w.Done <- w
			err := recv.Interface()
			if err != nil {
				errors[idx] = err.(error)
			}
			writers[idx] = nil
			cases[idx].Chan = disabled
			todo--
		}
	}

	return errors
}

// SendMessage starts a new message and returns an io.WriteCloser
// which can be used to send the message body.  The argument tp gives
// the message type (Text or Binary).  Text messages must be sent in
// utf-8 encoded form.
func (conn *Conn) SendMessage(tp MessageType) (io.WriteCloser, error) {
	if tp != Text && tp != Binary {
		return nil, ErrMessageType
	}
	w := <-conn.getFrameWriter
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

		msg := framePool.Get().(*frame)
		msg.Opcode = w.Opcode
		msg.Body = w.Buffer
		msg.Final = false
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
	msg := framePool.Get().(*frame)
	msg.Opcode = w.Opcode
	msg.Body = w.Buffer[:w.Pos]
	msg.Final = true
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
	switch {
	case l < 126:
		header[1] = byte(l)
	case l < (1 << 16):
		header[1] = 126
		header[2] = byte(l >> 8)
		header[3] = byte(l)
		n = 4
	default:
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
func (conn *Conn) writeMultiplexer(isFunctional chan<- struct{}) {
	writerDone := make(chan struct{})
	controlFrames := make(chan *frame, 1)
	fwChan := make(chan *frameWriter, 1)

	conn.writerDone = writerDone
	conn.sendControlFrame = controlFrames
	conn.getFrameWriter = fwChan
	close(isFunctional)

	dataBufferSize := conn.rw.Writer.Size() - maxHeaderSize
	if dataBufferSize < 512-maxHeaderSize {
		dataBufferSize = 512 - maxHeaderSize
	}
	dataFrames := make(chan *frame, 1)
	resChan := make(chan error, 1)
	w := &frameWriter{
		Buffer: make([]byte, dataBufferSize),
		Send:   dataFrames,
		Result: resChan,
		Done:   fwChan,
	}
	fwChan <- w

writerLoop:
	for {
		select {
		case frame := <-dataFrames:
			err := conn.writeFrame(frame.Opcode, frame.Body, frame.Final)
			resChan <- err

			frame.Body = nil
			framePool.Put(frame)
		case frame, ok := <-controlFrames:
			if !ok {
				break writerLoop
			}
			// TODO(voss): what to do, if there is a write error?
			conn.writeFrame(frame.Opcode, frame.Body, true)
			op := frame.Opcode

			frame.Body = nil
			framePool.Put(frame)

			if op == closeFrame {
				break writerLoop
			}
		}
	}

	// from this point onwards we don't write to the connection any more
	close(writerDone)

drainLoop:
	for {
		select {
		case <-fwChan:
			close(fwChan)
			fwChan = nil
		case <-dataFrames:
			resChan <- ErrConnClosed
		case _, ok := <-controlFrames:
			if !ok {
				break drainLoop
			}
		}
	}
}
