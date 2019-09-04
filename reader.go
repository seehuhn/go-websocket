package websocket

import (
	"io"
	"log"
)

func (conn *Conn) ReceiveMessage() (FrameType, io.Reader, error) {
	r, ok := <-conn.readMessage
	if !ok {
		return 0, nil, io.EOF
	}
	return r.Opcode, r, nil
}

type wsReader struct {
	Opcode  FrameType
	Buffers chan<- []byte
	Done    <-chan *ioResult
}

func (r *wsReader) Read(p []byte) (n int, err error) {
	r.Buffers <- p
	res := <-r.Done
	return res.N, res.Err
}

func (conn *Conn) readFrameHeader(buf []byte) (*frame, error) {
	n, err := conn.rw.Read(buf[:2])
	if err != nil {
		return nil, err
	} else if n < 2 {
		return nil, errFrameFormat
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
		n, _ := conn.rw.Read(buf[:lengthBytes])
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
	n, _ = conn.rw.Read(buf[:4])
	if n < 4 {
		return nil, errFrameFormat
	}

	return &frame{
		Final:  final != 0,
		Opcode: FrameType(opcode),
		Length: length,
		Mask:   buf[:4],
	}, nil
}

func (conn *Conn) handleControlFrame(opcode FrameType, body []byte) error {
	switch opcode {
	case closeFrame:
		code := 1005
		msg := ""
		if len(body) >= 2 {
			code = 256*int(body[0]) + int(body[1])
			msg = string(body[2:])
		}
		log.Println("CLOSE", code, msg)
	case pingFrame:
	case pongFrame:
	default:
		return errFrameOpcode
	}
	return nil
}

// readFrames reads all data from the network and creates a new
// wsReader{} for each message.  Control frames are handled internally
// by this function.
func (conn *Conn) readFrames(rcv chan<- *wsReader) {
	inMessage := false

	buf := make([]byte, 8)
	done := make(chan *ioResult)
	buffers := make(chan []byte)
readerLoop:
	for {
		frame, err := conn.readFrameHeader(buf)
		if err != nil {
			log.Println("framing error:", err)
			break
		}
		log.Println("read:", frame.Final, frame.Opcode, frame.Length)

		if frame.Opcode >= 8 {
			buf := make([]byte, frame.Length)
			n, err := io.ReadFull(conn.rw, buf)
			if err != nil {
				break readerLoop
			}
			for i := 0; i < n; i++ {
				buf[i] ^= frame.Mask[i%4]
			}
			err = conn.handleControlFrame(frame.Opcode, buf)
			if err != nil {
				break readerLoop
			}
			continue
		}

		if frame.Opcode == TextFrame || frame.Opcode == BinaryFrame {
			if inMessage {
				// handle the error
				break readerLoop
			}
			inMessage = true

			r := &wsReader{
				Opcode:  frame.Opcode,
				Buffers: buffers,
				Done:    done,
			}
			rcv <- r
		} else if frame.Opcode != contFrame || !inMessage {
			// handle the error
			break readerLoop
		}

		var pos uint64
		for pos < frame.Length {
			buf := <-buffers

			maxLen := frame.Length - pos
			bufLen64 := uint64(len(buf))
			if maxLen > bufLen64 {
				maxLen = bufLen64
			}
			n, _ := conn.rw.Read(buf[:maxLen])
			if n == 0 {
				// handle the error
				break readerLoop
			}

			offs := int(pos % 4)
			for i := 0; i < n; i++ {
				buf[i] ^= frame.Mask[(i+offs)%4]
			}

			res := &ioResult{
				N:   n,
				Err: nil,
			}
			done <- res

			pos += uint64(n)
		}
		if frame.Final {
			inMessage = false
		}
	}
	close(rcv)
}
