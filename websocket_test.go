// seehuhn.de/go/websocket - an http server to establish websocket connections
// Copyright (C) 2021  Jochen Voss <voss@seehuhn.de>
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
	"bufio"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
)

var (
	errTestUpgradeFailed = errors.New("protocol upgrade failed")
	errTestWrongResult   = errors.New("wrong result")
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
		// errors are expected here, when we shut down the server
		_ = http.Serve(listener, websocket)
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

func (client *TestClient) SendFrame(op MessageType, body []byte, final bool) error {
	l := len(body)
	buf := make([]byte, l+14)
	headerLength := client.MakeHeader(buf, op, uint64(l), final)
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

	err = client.SendFrame(Text, []byte(testMsg), true)
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

func TestClientStatusCode(t *testing.T) {
	type res struct {
		connInfo ConnInfo
		status   Status
		message  string
	}
	c := make(chan *res, 1)

	// server code
	handler := func(conn *Conn) {
		_, err := conn.ReceiveText(128)
		if err == ErrConnClosed {
			connInfo, status, message := conn.Wait()
			c <- &res{connInfo, status, message}
		} else {
			conn.Close(StatusProtocolError, "")
			t.Error("expected ErrConnClosed, got", err)
			c <- &res{ConnDropped, 9999, err.Error()}
		}
	}

	server, err := StartTestServer(handler)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	type testCase struct {
		sentCode        Status // client -> server
		sentMessage     string // client -> server
		expectedCode    Status
		expectedMessage string
	}
	cases := []*testCase{
		// good cases
		{StatusOK, "good bye", StatusOK, "good bye"},
		{4444, "good bye", 4444, "good bye"},
		{StatusNotSent, "", StatusNotSent, ""},
		{StatusDropped, "", StatusDropped, ""},

		{9999, "", StatusDropped, ""},     // invalid status code
		{9999, "test", StatusDropped, ""}, // invalid status code
	}

	for testNo, test := range cases {
		// fake client code
		client, err := server.Connect()
		if err != nil {
			t.Fatal(err)
		}

		if test.sentCode != StatusDropped {
			// send a close frame with status=test.sentCode
			var body []byte
			if test.sentCode != StatusNotSent {
				body = append(body, byte(test.sentCode>>8), byte(test.sentCode))
				body = append(body, []byte(test.sentMessage)...)
			}
			err = client.SendFrame(closeFrame, body, true)
			if err != nil {
				t.Fatal(err)
			}

			opcode, resp, err := client.ReadFrame()
			if opcode != closeFrame || err != nil {
				t.Fatal(err)
			}

			if test.sentCode == 9999 {
				// pass
			} else if len(resp) > 2 {
				t.Error("server invented a body: " + string(body))
			}
		}

		err = client.Close()
		if err != nil {
			t.Fatal(err)
		}

		recv := <-c
		if recv.status != test.expectedCode || recv.message != test.expectedMessage {
			t.Errorf("%d wrong status code/message: received %d/%s, expected %d/%s",
				testNo,
				recv.status, recv.message, test.expectedCode, test.expectedMessage)
		}
	}
}

func TestServerStatusCode(t *testing.T) {
	type res struct {
		connInfo ConnInfo
		status   Status
		message  string
	}
	c := make(chan *res, 1)
	handler := func(conn *Conn) {
		conn.Close(StatusOK, "penguin")
		connInfo, status, message := conn.Wait()
		c <- &res{connInfo, status, message}
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

	err = client.SendFrame(closeFrame, resp, true)
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

// TestLargeMessage tests whether large messages are transmitted correctly.
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

// TestKeepConn tests whether Conn can be used after the handler has
// terminated.
func TestKeepConn(t *testing.T) {
	keep := make(chan *Conn, 1)
	server, err := StartTestServer(func(c *Conn) {
		keep <- c
	})
	if err != nil {
		t.Fatal(err)
	}

	client, err := server.Connect()
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	go echo(<-keep)

	buf := make([]byte, 64)
	err = client.BounceBinary(17, buf, binaryLengthCheck(17))
	if err != nil {
		t.Error(err)
	}
}

func TestEchoMany(t *testing.T) {
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

	buf := make([]byte, 16)
	for i := 0; i < 1e6; i++ {
		err = client.BounceBinary(16, buf, binaryLengthCheck(16))
		if err != nil {
			t.Fatal(err)
		}
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
				_ = client.BounceBinary(size, buf, binaryLengthCheck(size))
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
			_, err = io.CopyBuffer(io.Discard, r, buf)
			if err != nil {
				fmt.Println("discard error:", err)
			}
			break
		}

		_, err = io.CopyBuffer(w, r, buf)
		if err != nil {
			fmt.Println("write error:", err)
			_, err = io.CopyBuffer(io.Discard, r, buf)
			if err != nil {
				fmt.Println("discard error:", err)
			}
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
