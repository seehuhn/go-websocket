package websocket

import (
	"bufio"
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"
	"io/ioutil"
	"net"
	"net/http"
	"strings"
	"testing"
)

const (
	errTestUpgradeFailed = webSocketError("protocol upgrade failed")
	errTestWrongResult   = webSocketError("wrong result")
)

type TestServer struct {
	addr     *net.UnixAddr
	listener *net.UnixListener
}

// StartTestServer starts a websocket server which calls `handler`
// to handle connections.  Clients can be connected using the .Connect()
// method.
func StartTestServer(handler func(*Conn)) (*TestServer, error) {
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
		websocket := &Handler{
			Handle: handler,
		}
		http.Serve(listener, websocket)
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

func (client *TestClient) MakeHeader(buf []byte, op MessageType, l uint64, final bool) int {
	buf[0] = byte(op)
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
	return headerLength
}

func (client *TestClient) SendFrame(op MessageType, body []byte) error {
	l := len(body)
	buf := make([]byte, l+14)
	headerLength := client.MakeHeader(buf, op, uint64(l), true)
	n := copy(buf[headerLength:], body)
	_, err := client.conn.Write(buf[:headerLength+n])
	return err
}

func (client *TestClient) SendNonsenseFrame(buf []byte, op MessageType, l uint64, final bool) error {
	headerLength := client.MakeHeader(buf, op, l, final)

	l += uint64(headerLength)
	for l > 0 {
		chunk := len(buf)
		if l < uint64(chunk) {
			chunk = int(l)
		}
		// Except for the header, frame contents don't matter here, so
		// we re-use buf over and over, until the required length has
		// been reached.
		n, err := client.conn.Write(buf[:chunk])
		if err != nil {
			return err
		}
		l -= uint64(n)
	}
	return nil
}

func (client *TestClient) ReadHeader() (MessageType, uint64, bool, error) {
	h1, err := client.reader.ReadByte()
	if err != nil {
		return 0, 0, true, err
	}
	opcode := h1 & 15

	h2, err := client.reader.ReadByte()
	if err != nil {
		return 0, 0, true, err
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
		return 0, 0, true, err
	}
	return MessageType(opcode), length, h1&128 != 0, err
}

func (client *TestClient) ReadFrame() (MessageType, []byte, error) {
	opcode, length, _, err := client.ReadHeader()
	if err != nil {
		return opcode, nil, err
	}
	if length > 1024*1024 {
		return opcode, nil, ErrTooLarge
	}

	body := make([]byte, length)
	_, err = io.ReadFull(client.reader, body)
	return opcode, body, err
}

func (client *TestClient) DiscardFrame() (MessageType, uint64, error) {
	opcode, length, final, err := client.ReadHeader()
	if err != nil {
		return 0, 0, err
	}

	// discard the message contents
	err = client.Discard(length)
	if err != nil {
		return 0, 0, err
	}

	if final {
		err = io.EOF
	}
	return opcode, length, err
}

func (client *TestClient) BounceBinary(length uint64, buffer []byte,
	checkFun func(MessageType, uint64) error) error {
	readerDone := make(chan error, 1)
	go func() {
		var total uint64
		var msgType MessageType = 255
		for {
			op, n, err := client.DiscardFrame()
			total += n
			if msgType == 255 {
				msgType = op
			}
			if err == io.EOF {
				break
			} else if err != nil {
				readerDone <- err
				return
			}
		}
		readerDone <- checkFun(msgType, total)
	}()

	todo := length
	maxChunk := uint64(len(buffer) - 14)
	var sendErr error
	var op MessageType = Binary
	for {
		nextChunk := todo
		if nextChunk > maxChunk {
			nextChunk = maxChunk
		}

		sendErr = client.SendNonsenseFrame(buffer, op, nextChunk, nextChunk == todo)
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

// TestServerToClient tries to send a single message from the server
// to a client.
func TestServerToClient(t *testing.T) {
	const testMsg = "testing, testing, testing ..."

	var sErr error
	server, err := StartTestServer(func(c *Conn) {
		sErr = c.SendText(testMsg)
		c.Close(StatusOK, "")
	})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	client, err := server.Connect()
	if err != nil {
		t.Fatal(err)
	}

	tp, msg, err := client.ReadFrame()
	if err != nil {
		t.Fatal(err)
	}
	if tp != Text {
		t.Fatal("wrong message type")
	}
	if string(msg) != testMsg {
		t.Error("wrong message")
	}

	if sErr != nil {
		t.Error("server error:", sErr)
	}
}

// TestClientToServer tries to send a single message from a client
// to the server.
func TestClientToServer(t *testing.T) {
	const testMsg = "test"

	var sMsg string
	sErr := make(chan error, 1)
	server, err := StartTestServer(func(c *Conn) {
		var err error
		sMsg, err = c.ReceiveText(64)
		sErr <- err
		c.Close(StatusOK, "")
	})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	client, err := server.Connect()
	if err != nil {
		t.Fatal(err)
	}

	err = client.SendFrame(Text, []byte(testMsg))
	if err != nil {
		t.Fatal(err)
	}

	err = <-sErr
	if err != nil {
		t.Fatal(err)
	}

	if sMsg != testMsg {
		t.Errorf("wrong message: %q != %q", sMsg, testMsg)
	}
}

// TestDiscard tests that too long messages are correctly processed
// on the server side.
func TestDiscard(t *testing.T) {
	var serverError string
	server, err := StartTestServer(func(conn *Conn) {
		// We send messages of 300 bytes length, but only use a buffer of 150
		// bytes.  Make sure ErrTooLarge is reported.
		msg := make([]byte, 150)
		status := StatusOK
		for {
			n, err := conn.ReceiveBinary(msg)
			if err == ErrConnClosed {
				return
			} else if err != ErrTooLarge {
				serverError = "errTooLarge not reported"
				status = StatusProtocolError
				break
			}
			err = conn.SendBinary(msg[:n])
			if err != nil {
				serverError = "server error: " + err.Error()
				status = StatusProtocolError
				break
			}
		}
		conn.Close(status, "")
	})
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	client, err := server.Connect()
	if err != nil {
		t.Fatal(err)
	}

	buf := make([]byte, 100)
	for i := 0; i < 10; i++ {
		// Repeat the test, to make sure that the server drains the unread
		// part of the message and does not hang.
		err = client.BounceBinary(300, buf, binaryLengthCheck(150))
		if err != nil {
			t.Fatal(err)
		}
	}

	err = client.Close()
	if err != nil {
		t.Error(err)
	}
	if serverError != "" {
		t.Error(serverError)
	}
}

func TestLargeMessage(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping test in short mode.")
	}

	server, err := StartTestServer(echo)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	client, err := server.Connect()
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	buf := make([]byte, 16*1024)

	const long = 1<<32 + 1
	err = client.BounceBinary(long, buf, binaryLengthCheck(long))
	if err != nil {
		t.Fatal(err)
	}
}

func TestClientStatusCode(t *testing.T) {
	type res struct {
		status  Status
		message string
	}
	c := make(chan *res, 1)
	handler := func(conn *Conn) {
		_, err := conn.ReceiveText(128)
		if err == ErrConnClosed {
			status, message := conn.GetStatus()
			c <- &res{status, message}
		} else {
			conn.Close(StatusProtocolError, "")
			t.Error("expected ErrConnClosed, got", err)
			c <- &res{9999, err.Error()}
		}
	}

	server, err := StartTestServer(handler)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	type testCase struct {
		s1 Status
		m1 string
		s2 Status
		m2 string
	}
	cases := []*testCase{
		{StatusOK, "good bye", StatusOK, "good bye"},
		{4444, "good bye", 4444, "good bye"},
		{StatusNotSent, "", StatusNotSent, ""},
		{9999, "", StatusNotSent, ""},
		{0, "", StatusDropped, ""},
	}

	for idx, test := range cases {
		fmt.Println("test case", idx)
		client, err := server.Connect()
		if err != nil {
			t.Fatal(err)
		}

		if test.s1 > 0 {
			var body []byte
			if test.s1 != 1005 {
				body = append(body, byte(test.s1>>8), byte(test.s1))
				body = append(body, []byte(test.m1)...)
			}
			err = client.SendFrame(8, body)
			if err != nil {
				t.Fatal(err)
			}
			opcode, resp, err := client.ReadFrame()
			if opcode != 8 || err != nil {
				t.Fatal(err)
			}
			if test.s1 == 9999 {
				// pass
			} else if test.s1 != 1005 {
				if !bytes.Equal(body, resp) {
					t.Error("wrong status code/message sent by server")
				}
			} else if len(body) > 0 {
				t.Error("server invented a body")
			}
		}
		err = client.Close()
		if err != nil {
			t.Fatal(err)
		}
		serverRes := <-c
		fmt.Printf(". sent %d/%q, expected %d/%q, received %d/%q\n",
			test.s1, test.m1, test.s2, test.m2, serverRes.status, serverRes.message)
		if serverRes.status != test.s2 || serverRes.message != test.m2 {
			t.Error("wrong status code/message recorded by server")
		}
	}
	fmt.Println("test cases done")
}

func TestServerStatusCode(t *testing.T) {
	type res struct {
		status  Status
		message string
	}
	c := make(chan *res, 1)
	handler := func(conn *Conn) {
		conn.Close(StatusOK, "penguin")
		status, message := conn.GetStatus()
		c <- &res{status, message}
	}

	server, err := StartTestServer(handler)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	client, err := server.Connect()
	if err != nil {
		t.Fatal(err)
	}

	opcode, resp, err := client.ReadFrame()
	if opcode != 8 || err != nil || len(resp) < 2 {
		t.Fatal(err)
	}
	status := Status(resp[0])<<8 + Status(resp[1])
	msg := string(resp[2:])
	if status != StatusOK || msg != "penguin" {
		t.Error("client received wrong status code/message")
	}

	err = client.SendFrame(8, resp)
	if err != nil {
		t.Fatal(err)
	}

	err = client.Close()
	if err != nil {
		t.Fatal(err)
	}

	serverRes := <-c
	if serverRes.status != StatusOK || serverRes.message != "penguin" {
		t.Error("wrong status code/message recorded by server")
	}
}

func BenchmarkEcho(b *testing.B) {
	server, err := StartTestServer(echo)
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
	err = client.BounceBinary(10, buf, binaryLengthCheck(10))
	if err != nil {
		b.Fatal(err)
	}

	for _, size := range []uint64{0, 1 << 6, 1 << 12, 1 << 18} {
		b.Run(fmt.Sprintf("echo%d", size), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				client.BounceBinary(size, buf, binaryLengthCheck(size))
			}
		})
	}

	// test whether the connection survived the load test
	err = client.BounceBinary(10, buf, binaryLengthCheck(10))
	if err != nil {
		b.Fatal(err)
	}
}

func echo(conn *Conn) {
	defer conn.Close(StatusOK, "")

	buf := make([]byte, 16*1024)
	for {
		tp, r, err := conn.ReceiveMessage()
		if err == ErrConnClosed {
			break
		} else if err != nil {
			fmt.Println("read error:", err)
			break
		}

		w, err := conn.SendMessage(tp)
		if err != nil {
			fmt.Println("cannot create writer:", err)
			// We need to read the complete message, so that the next
			// read doesn't block.
			io.CopyBuffer(ioutil.Discard, r, buf)
			break
		}

		_, err = io.CopyBuffer(w, r, buf)
		if err != nil {
			fmt.Println("write error:", err)
			io.CopyBuffer(ioutil.Discard, r, buf)
		}

		err = w.Close()
		if err != nil && err != ErrConnClosed {
			fmt.Println("close error:", err)
		}
	}
}

func binaryLengthCheck(l uint64) func(MessageType, uint64) error {
	return func(op MessageType, length uint64) error {
		if op != Binary || length != l {
			return errTestWrongResult
		}
		return nil
	}
}
