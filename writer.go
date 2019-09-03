package websocket

import "io"

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

func (conn *Conn) writeFrameHeader(buf []byte, frame *frame) error {
	buf[0] = byte(frame.Opcode)

	if frame.Final {
		buf[0] |= 128
	}

	l := frame.Length
	n := 2
	if l < 126 {
		buf[1] = byte(l)
	} else if l < (1 << 16) {
		buf[1] = 126
		buf[2] = byte(l >> 8)
		buf[3] = byte(l)
		n = 4
	} else {
		buf[1] = 127
		buf[2] = byte(l >> 56)
		buf[3] = byte(l >> 48)
		buf[4] = byte(l >> 40)
		buf[5] = byte(l >> 32)
		buf[6] = byte(l >> 24)
		buf[7] = byte(l >> 16)
		buf[8] = byte(l >> 8)
		buf[9] = byte(l)
		n = 10
	}

	_, err := conn.rw.Write(buf[:n])
	return err
}

// writeFrames multiplexes all output to the network.
func (conn *Conn) writeFrames(data <-chan *writerSlot, ctl <-chan *controlMsg) {
	headerBuffer := make([]byte, 10)

	done := make(chan ioResult, 1)

	var dataOpcode FrameType
	var dataBuffersIn <-chan []byte
	dataBuffer := make([]byte, 0, 16*1024)
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
				if dataBufferPos == len(dataBuffer) || final {
					frame := &frame{
						Final:  final,
						Opcode: dataOpcode,
						Length: uint64(len(dataBuffer)),
					}
					err := conn.writeFrameHeader(headerBuffer, frame)
					if err != nil {
						// TODO(voss): handle the error
						break writerLoop
					}
					_, err = conn.rw.Write(dataBuffer)
					if err != nil {
						// TODO(voss): handle the error
						break writerLoop
					}
					conn.rw.Flush()
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
			frame := &frame{
				Final:  true,
				Opcode: ctlMsg.Opcode,
				Length: uint64(len(ctlMsg.Body)),
			}
			err := conn.writeFrameHeader(headerBuffer, frame)
			if err != nil {
				// TODO(voss): handle the error
				break writerLoop
			}
			_, err = conn.rw.Write(ctlMsg.Body)
			if err != nil {
				// TODO(voss): handle the error
				break writerLoop
			}
			conn.rw.Flush()
		}
	}
}
