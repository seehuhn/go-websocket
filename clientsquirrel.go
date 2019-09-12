// +build ignore

package main

import (
	"bufio"
	"fmt"
	"io"
	"log"
	"net"
	"strings"
)

func makeFrameHeader(buf []byte, opcode byte, length int, final bool) int {
	buf[0] = opcode
	if final {
		buf[0] |= 128
	}

	l := uint64(length)
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

	// we have to use a mask
	buf[1] |= 128
	for i := 0; i < 4; i++ {
		buf[n] = 0
		n++
	}

	return n
}

func main() {
	conn, err := net.Dial("tcp", "localhost:8080")
	if err != nil {
		log.Fatal(err)
	}
	defer conn.Close()

	fmt.Fprintf(conn, "GET /chat HTTP/1.1\r\n"+
		"Host: localhost\r\n"+
		"Upgrade: websocket\r\n"+
		"Connection: Upgrade\r\n"+
		"Sec-WebSocket-Key: 0000000000000000000000==\r\n"+
		"Sec-WebSocket-Version: 13\r\n\r\n")
	rw := bufio.NewReader(conn)
	for {
		line, err := rw.ReadString('\n')
		if err != nil {
			log.Fatal(err)
		}
		line = strings.TrimSpace(line)
		if line == "" {
			break
		}
		fmt.Println(line)
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

	_, err = io.ReadFull(rw, recv[:n-4]) // no mask
	if err != nil {
		log.Fatal(err)
	}

}
