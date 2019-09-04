package websocket

import (
	"io"
)

func (conn *Conn) WriteMessage(tp FrameType) (io.WriteCloser, error) {
	if tp != TextFrame && tp != BinaryFrame {
		return nil, errFrameType
	}

	buffers := make(chan []byte, 1)
	getDone := make(chan (<-chan ioResult))

	conn.getWriter <- &writerSlot{
		Opcode:  tp,
		Buffers: buffers,
		GetDone: getDone,
	}
	done := <-getDone
	if done == nil {
		return nil, ErrConnClosed
	}

	return &wsWriter{
		Buffers: buffers,
		Done:    done,
	}, nil
}

type writerSlot struct {
	Opcode  FrameType
	Buffers <-chan []byte
	GetDone chan<- <-chan ioResult
}

type wsWriter struct {
	Buffers chan<- []byte
	Done    <-chan ioResult
}

func (w *wsWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	w.Buffers <- p
	res := <-w.Done
	return res.N, res.Err
}

func (w *wsWriter) Close() error {
	close(w.Buffers)
	res := <-w.Done
	return res.Err
}

func (conn *Conn) writeFrame(opcode FrameType, body []byte, final bool) error {
	var header [10]byte

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

// writeFrames multiplexes all output to the network.
func (conn *Conn) writeFrames(data <-chan *writerSlot, ctl <-chan *controlMsg) {
	done := make(chan ioResult, 1)

	var dataOpcode FrameType
	var dataBuffersIn <-chan []byte
	dataBufferSize := conn.rw.Writer.Size()
	if dataBufferSize < 512 {
		dataBufferSize = 512
	}
	dataBuffer := make([]byte, dataBufferSize)
	dataBufferPos := 0

	getWriter := data
writerLoop:
	for {
		// wait for one of the following:
		// - new client connecting via conn.getWriter
		// - existing client sending data via the buffers channel
		// - existing client closing the buffers channel
		// - a control message being sent
		select {
		case slot := <-getWriter:
			dataOpcode = slot.Opcode
			dataBuffersIn = slot.Buffers
			dataBufferPos = 0
			slot.GetDone <- done

			// do not accept new writers until this one is done
			getWriter = nil

		case buf, ok := <-dataBuffersIn:
			total := 0
			for {
				n := copy(dataBuffer[dataBufferPos:], buf)
				total += n
				dataBufferPos += n
				buf = buf[n:]

				final := len(buf) == 0 && !ok
				if dataBufferPos == dataBufferSize || final {
					err := conn.writeFrame(dataOpcode, dataBuffer[:dataBufferPos], final)
					if err != nil {
						// TODO(voss): handle the error
						break writerLoop
					}
					dataBufferPos = 0
					dataOpcode = contFrame
				}

				if len(buf) == 0 {
					break
				}
			}
			done <- ioResult{N: total}
			if !ok {
				dataBuffersIn = nil
				getWriter = data
			}
		case ctlMsg := <-ctl:
			err := conn.writeFrame(ctlMsg.Opcode, ctlMsg.Body, true)
			if err != nil {
				// TODO(voss): handle the error
				break writerLoop
			}
			conn.rw.Flush()
		}
	}
}
