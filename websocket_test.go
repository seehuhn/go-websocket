package websocket

import (
	"bufio"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"strings"
	"testing"
)

const (
	errTestUpgradeFailed = webSocketError("protocol upgrade failed")
	errTestWrongLength   = webSocketError("wrong length")
	errWrongOpcode       = webSocketError("wrong opcode")
)

type TestServer struct {
	addr     *net.UnixAddr
	listener *net.UnixListener
}

func StartTestServer() (*TestServer, error) {
	nonce := make([]byte, 8)
	_, err := rand.Read(nonce)
	if err != nil {
		return nil, err
	}
	socketName := fmt.Sprintf("/tmp/ws%02x", nonce)

	addr, err := net.ResolveUnixAddr("unix", socketName)
	if err != nil {
		return nil, err
	}

	listener, err := net.ListenUnix("unix", addr)
	if err != nil {
		return nil, err
	}

	// start the websocket server
	go func() {
		log.Println("server listening")
		websocket := &Handler{
			Handle: echo,
		}
		http.Serve(listener, websocket)
		log.Println("server terminated")
	}()

	return &TestServer{
		addr:     addr,
		listener: listener,
	}, nil
}

func (server *TestServer) Close() error {
	return server.listener.Close()
}

func (server *TestServer) Connect() (*TestClient, error) {
	conn, err := net.DialUnix("unix", nil, server.addr)
	if err != nil {
		return nil, err
	}

	msg := []byte("GET /chat HTTP/1.1\r\n" +
		"Host: localhost\r\n" +
		"Upgrade: websocket\r\n" +
		"Connection: Upgrade\r\n" +
		"Sec-WebSocket-Key: 0000000000000000000000==\r\n" +
		"Sec-WebSocket-Version: 13\r\n\r\n")
	_, err = conn.Write(msg)
	if err != nil {
		conn.Close()
		return nil, err
	}

	reader := bufio.NewReader(conn)
	good := false
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			conn.Close()
			return nil, err
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
		conn.Close()
		return nil, errTestUpgradeFailed
	}

	return &TestClient{
		conn:   conn,
		reader: reader,
	}, nil
}

type TestClient struct {
	conn   net.Conn
	reader *bufio.Reader
}

func (client *TestClient) Close() error {
	return client.conn.Close()
}

func (client *TestClient) Discard(n uint64) error {
	const maxChunk = 1024 * 1024
	for n > 0 {
		chunk := n
		if chunk > maxChunk {
			chunk = maxChunk
		}
		done, err := client.reader.Discard(int(chunk))
		if err != nil {
			return err
		}
		n -= uint64(done)
	}
	return nil
}

func (client *TestClient) SendFrame(buf []byte, op byte, l uint64, final bool) error {
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

	l += uint64(headerLength)
	for l > 0 {
		chunk := len(buf)
		if l < uint64(chunk) {
			chunk = int(l)
		}
		// Except for the header, frame contents don't matter here, so
		// we reuse buf over and over, until the required length has
		// been reached.
		n, err := client.conn.Write(buf[:chunk])
		if err != nil {
			return err
		}
		l -= uint64(n)
	}
	return nil
}

func (client *TestClient) ReadFrame() (byte, uint64, error) {
	h1, err := client.reader.ReadByte()
	if err != nil {
		return 0, 0, err
	}
	opcode := h1 & 15

	h2, err := client.reader.ReadByte()
	if err != nil {
		return 0, 0, err
	}
	l0 := h2 & 127

	var length uint64
	switch l0 {
	case 127:
		err = binary.Read(client.reader, binary.BigEndian, &length)
	case 126:
		var l16 uint16
		err = binary.Read(client.reader, binary.BigEndian, &l16)
		length = uint64(l16)
	default:
		length = uint64(l0)
		err = nil
	}
	if err != nil {
		return 0, 0, err
	}

	// discard the message contents
	err = client.Discard(length)
	if err != nil {
		return 0, 0, err
	}

	if h1&128 != 0 {
		err = io.EOF
	}
	return opcode, length, err
}

func (client *TestClient) BounceBinary(length uint64, buffer []byte) error {
	readerDone := make(chan error, 1)
	go func() {
		var total uint64
		var expectOp byte = 2
		for {
			op, n, err := client.ReadFrame()
			total += n
			if err == io.EOF {
				break
			} else if err != nil {
				readerDone <- err
				return
			} else if op != expectOp {
				readerDone <- errWrongOpcode
				return
			}
			expectOp = 0
		}
		if total != length {
			readerDone <- errTestWrongLength
			return
		}
		readerDone <- nil
	}()

	todo := length
	maxChunk := uint64(len(buffer) - 14)
	var sendErr error
	var op byte = 2
	for {
		nextChunk := todo
		if nextChunk > maxChunk {
			nextChunk = maxChunk
		}

		sendErr = client.SendFrame(buffer, op, nextChunk, nextChunk == todo)
		todo -= nextChunk
		op = 0
		if sendErr != nil || todo == 0 {
			break
		}
	}
	recvErr := <-readerDone

	if sendErr == nil {
		sendErr = recvErr
	}
	return sendErr
}

func echo(conn *Conn) {
	defer conn.Close(StatusOK, "")

	buf := make([]byte, 16*1024)
	for {
		tp, r, err := conn.ReceiveMessage()
		if err == ErrConnClosed {
			break
		} else if err != nil {
			log.Println("read error:", err)
			break
		}

		w, err := conn.SendMessage(tp)
		if err != nil {
			log.Println("cannot create writer:", err)
			// We need to read the complete message, so that the next
			// read doesn't block.
			io.CopyBuffer(ioutil.Discard, r, buf)
			break
		}

		_, err = io.CopyBuffer(w, r, buf)
		if err != nil {
			log.Println("write error:", err)
			io.CopyBuffer(ioutil.Discard, r, buf)
		}

		err = w.Close()
		if err != nil && err != ErrConnClosed {
			log.Println("close error:", err)
		}
	}
}

func BenchmarkEcho(b *testing.B) {
	server, err := StartTestServer()
	if err != nil {
		b.Fatal(err)
	}
	defer server.Close()

	client, err := server.Connect()
	if err != nil {
		b.Fatal(err)
	}
	defer client.Close()

	buf := make([]byte, 16*1024)

	// test whether the connection is functional
	err = client.BounceBinary(10, buf)
	if err != nil {
		log.Fatal(err)
	}

	for _, size := range []uint64{0, 1 << 6, 1 << 12, 1 << 18, 1 << 31} {
		b.Run(fmt.Sprintf("echo%d", size), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				client.BounceBinary(size, buf)
			}
		})
	}

	// test whether the connection survived the load test
	err = client.BounceBinary(10, buf)
	if err != nil {
		log.Fatal(err)
	}
}
