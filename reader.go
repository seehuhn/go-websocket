package websocket

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"io/ioutil"
	"log"
)

func (conn *Conn) ReceiveBinary(maxLength int) ([]byte, error) {
	buf, err := conn.receiveLimited(Binary, maxLength)
	return buf, err
}

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
	if tp == ex {
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
		header := <-r.Receive
		if header == nil {
			r.Final = true
		} else {
			r.Pos = 0
			r.Length = header.Length
			r.Final = header.Final
			r.Mask = header.Mask
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

	fmt.Println("R", opcode, length, map[bool]string{true: "final"}[final != 0])

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
	var headerBuf [10]byte
readLoop:
	for {
		header, err := conn.readFrameHeader(headerBuf[:])
		if err != nil {
			if err == errFrameFormat {
				status = StatusProtocolError
			} else {
				fmt.Printf("READ ERROR: %#v\n", err)
			}
			break readLoop
		}

		switch header.Opcode {
		case Text, Binary:
			dfChan <- header
			<-resChan
		case closeFrame:
			buf, _ := conn.readFrameBody(header)
			// we are exiting anyway, so we don't need to check for errors here
			if len(buf) >= 2 {
				status = 256*Status(buf[0]) + Status(buf[1])
				closeMessage = string(buf[2:])
			} else {
				status = statusMissing
				closeMessage = ""
			}
			log.Println("CLOSE", status, closeMessage)
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
			closeMessage = fmt.Sprintf("invalid opcode %d", header.Opcode)
			break readLoop
		}
	}

	// disable further receives
	close(dfChan)

	// Sending a close response unconditionally is safe,
	// since the server will ignore everything after it has
	// sent a close message once.
	conn.sendCloseFrame(status, []byte(closeMessage))
	log.Println("readers done")
}
