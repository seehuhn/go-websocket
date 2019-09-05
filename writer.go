package websocket

import (
	"fmt"
	"io"
	"log"
)

const maxHeaderSize = 10

func (conn *Conn) SendText(msg string) error {
	return conn.sendData(TextFrame, []byte(msg))
}

func (conn *Conn) SendBinary(msg []byte) error {
	return conn.sendData(BinaryFrame, msg)
}

func (conn *Conn) sendData(opcode FrameType, data []byte) error {
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

func (conn *Conn) SendMessage(tp FrameType) (io.WriteCloser, error) {
	if tp != TextFrame && tp != BinaryFrame {
		return nil, errFrameType
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
	Opcode FrameType
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

func (conn *Conn) writeFrame(opcode FrameType, body []byte, final bool) error {
	fmt.Println("W", opcode, len(body), map[bool]string{true: "final"}[final])

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
	cfChan := make(chan *frame)
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
			// TODO(voss): handle write errors?

			if frame.Opcode == closeFrame {
				break writerLoop
			}
		}
	}
	// from this point onwards we don't write to the connection any more

	log.Println("draining writers")
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
	log.Println("writers done")
}
