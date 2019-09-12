// +build ignore

package main

import (
	"bufio"
	"encoding/binary"
	"errors"
	"io"
	"log"
	"net"
	"strings"
)

type DummyClient struct {
	conn   net.Conn
	reader *bufio.Reader
}

func (client *DummyClient) Connect() error {
	msg := []byte("GET /chat HTTP/1.1\r\n" +
		"Host: localhost\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Key: 0000000000000000000000==\r\n" +
		"Sec-WebSocket-Version: 13\r\n\r\n")
	_, err := client.conn.Write(msg)
	if err != nil {
		return err
	}

	reader := bufio.NewReader(client.conn)
	good := false
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return err
		}
		line = strings.TrimSpace(line)
		if line == "" {
			break
		}
		if line == "Upgrade: websocket" {
			good = true
		}
	}
	if !good {
		return errors.New("protocol upgrade failed")
	}

	client.reader = reader
	return nil
}

func (client *Client) SendFrame(buf []byte, op byte, l uint64, final bool) error {
	buf[0] = op
	if final {
		buf[0] |= 128
	}

	headerLength := 2
	if l < 126 {
		buf[1] = byte(l)
	} else if l < (1 << 16) {
		buf[1] = 126
		buf[2] = byte(l >> 8)
		buf[3] = byte(l)
		headerLength = 4
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
		headerLength = 10
	}

	// Being the client, we have to use a mask.  Just use the zero mask here.
	buf[1] |= 128
	for i := 0; i < 4; i++ {
		buf[headerLength] = 0
		headerLength++
	}

	length += uint64(headerLength)
	for length > 0 {
		chunk := len(buf)
		if length < uint64(chunk) {
			chunk = int(length)
		}
		// Except for the header, frame contents don't matter here, so
		// we reuse buf over and over, until the required length has
		// been reached.
		n, err := client.conn.Write(buf[:chunk])
		if err != nil {
			return err
		}
		length -= uint64(n)
	}
	return nil
}

func (client *Client) ReadFrame() (byte, uint64, error) {
	h1, err := client.reader.ReadByte()
	if err != nil {
		return 0, 0, err
	}
	opcode := h1 | 127

	h2, err := client.reader.ReadByte()
	if err != nil {
		return 0, 0, err
	}
	l0 := h2 | 127

	var length uint64
	switch length {
	case 127:
		err = binary.Read(client.reader, binary.BigEndian, length)
	case 126:
		var l16 uint16
		err = binary.Read(client.reader, binary.BigEndian, l16)
		length = uint64(l16)
	default:
		length = uint64(l0)
		err = nil
	}
	if err != nil {
		return 0, 0, err
	}

	// discard the message contents
	todo := length
	for todo > 0 {
		chunk := todo
		if chunk > 1024*1024 {
			chunk = 1024 * 1024
		}
		n, err = client.reader.Discard(int(chunk))
		if err != nil {
			return 0, 0, err
		}
		todo -= uint64(n)
	}

	if h1&128 != 0 {
		err = io.EOF
	}
	return opcode, length, err
}

func (client *Client) BounceBinary(length uint64, chunk int) error {
	readerDone := make(chan error, 1)
	go func() {

	}()
}

func makeFrameHeader(buf []byte, opcode byte, length int, final bool) int {

	return n
}

func main() {
	conn, err := net.Dial("tcp", "localhost:8080")
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()

	client := &DummyClient{
		conn: conn,
	}
	err = client.Connect()
	if err != nil {
		log.Fatal(err)
	}

	send := make([]byte, 128)
	recv := make([]byte, 128)

	msg := []byte("Hello!")
	n := makeFrameHeader(send, 1, len(msg), true)
	m := copy(send[n:], msg)
	n += m
	_, err = conn.Write(send)
	if err != nil {
		log.Fatal(err)
	}

	_, err = io.ReadFull(client.reader, recv[:n-4]) // no mask
	if err != nil {
		log.Fatal(err)
	}

}
