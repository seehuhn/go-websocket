// seehuhn.de/go/websocket - an http server to establish websocket connections
// Copyright (C) 2019  Jochen Voss <voss@seehuhn.de>
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
	"bytes"
	"fmt"
	"testing"

	"go.uber.org/goleak"
)

func TestReceiveBinary(t *testing.T) {
	defer goleak.VerifyNone(t)

	errorsInServer := make(chan string, 10)
	handler := func(conn *Conn) {
		// server code

		buf := make([]byte, 2)

		n, err := conn.ReceiveBinary(buf)
		if err != nil || n != 1 || buf[0] != 1 {
			errorsInServer <- fmt.Sprintf("read 1 failed: buf=[% x], err=%s", buf[:n], err)
		}

		n, err = conn.ReceiveBinary(buf)
		if err != ErrTooLarge || n != 2 || buf[0] != 4 {
			errorsInServer <- fmt.Sprintf("read 4 failed: buf=[% x], err=%s", buf[:n], err)
		}

		n, err = conn.ReceiveBinary(buf)
		if err != nil || n != 2 || buf[0] != 5 {
			errorsInServer <- fmt.Sprintf("read 5 failed: buf=[% x], err=%s", buf[:n], err)
		}

		n, err = conn.ReceiveBinary(buf)
		if err != ErrConnClosed || n != 0 {
			errorsInServer <- fmt.Sprintf("not properly closed: buf=[% x], err=%s", buf[:n], err)
		}

		err = conn.Close(StatusOK, "OK")
		if err != nil {
			errorsInServer <- err.Error()
		}

		close(errorsInServer)
	}

	server, err := StartTestServer(handler)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	// fake client
	client, err := server.Connect()
	if err != nil {
		t.Fatal(err)
	}

	// send one byte
	err = client.SendFrame(Binary, []byte{1}, true)
	if err != nil {
		t.Fatal(err)
	}

	// too long message
	err = client.SendFrame(Binary, []byte{4, 4, 4, 4}, true)
	if err != nil {
		t.Fatal(err)
	}

	// send two bytes
	err = client.SendFrame(Binary, []byte{5, 5}, true)
	if err != nil {
		t.Error(err)
	}

	err = client.Close()
	if err != nil {
		t.Error(err)
	}

	for err := range errorsInServer {
		t.Error("server: " + err)
	}
}

func TestReceiveEmpty(t *testing.T) {
	defer goleak.VerifyNone(t)

	errorsInServer := make(chan string, 5)
	handler := func(conn *Conn) {
		// server code

		buf := []byte{100, 101, 102, 103, 104, 105}

		n, err := conn.ReceiveBinary(buf)
		if err != nil {
			errorsInServer <- "receive error: " + err.Error()
		}
		if n != 0 {
			errorsInServer <- fmt.Sprintf("wrong length: %d", n)
		}
		if buf[0] != 100 {
			errorsInServer <- fmt.Sprintf("wrong buffer: %v", buf)
		}

		err = conn.Close(StatusOK, "")
		if err != nil {
			errorsInServer <- "close error: " + err.Error()
		}

		close(errorsInServer)
	}

	server, err := StartTestServer(handler)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	// fake client
	client, err := server.Connect()
	if err != nil {
		t.Fatal(err)
	}
	err = client.SendFrame(Binary, []byte{}, true) // send empty binary message
	if err != nil {
		t.Fatal(err)
	}

	tp, _, err := client.ReadFrame()
	if err != nil {
		t.Fatal(err)
	}
	if tp != closeFrame {
		t.Errorf("expected close frame, got %s", tp)
	}

	err = client.Close()
	if err != nil {
		t.Fatal(err)
	}

	for msg := range errorsInServer {
		t.Error(msg)
	}
}

// TestReceivePartial tests the case where a close frame is received
// in the middle of a message.
func TestReceivePartial(t *testing.T) {
	defer goleak.VerifyNone(t)

	errorsInServer := make(chan string, 10)
	handler := func(conn *Conn) {
		// server code

		buf := make([]byte, 16)

		tp, r, err := conn.ReceiveMessage()
		if err != nil {
			errorsInServer <- "ReceiveMessage: " + err.Error()
		} else if tp != Binary {
			errorsInServer <- fmt.Sprintf("ReceiveMessage: wrong message type: %s", tp)
		}

		n, err := r.Read(buf)
		if err != nil {
			errorsInServer <- "Read: " + err.Error()
		} else if !bytes.Equal(buf[:n], []byte{1, 2}) {
			errorsInServer <- fmt.Sprintf("Read: wrong data %v", buf[:n])
		}

		n, err = r.Read(buf)
		if err == nil {
			errorsInServer <- fmt.Sprintf("Read: expected error, got %d bytes", n)
		} else if err != ErrConnClosed {
			errorsInServer <- "Read: unexpected error" + err.Error()
		}
		if n != 0 {
			errorsInServer <- fmt.Sprintf("Read: wrong length %d", n)
		}

		err = conn.Close(StatusOK, "OK")
		if err != nil {
			errorsInServer <- "Close: " + err.Error()
		}

		close(errorsInServer)
	}

	server, err := StartTestServer(handler)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	// fake client
	client, err := server.Connect()
	if err != nil {
		t.Fatal(err)
	}

	// send first part of the message
	err = client.SendFrame(Binary, []byte{1, 2}, false)
	if err != nil {
		t.Fatal(err)
	}

	err = client.SendFrame(closeFrame, []byte{1000 / 256, 1000 % 256}, true)
	if err != nil {
		t.Fatal(err)
	}

	// send the second part of the message
	err = client.SendFrame(Binary, []byte{3, 4}, true)
	if err != nil {
		t.Fatal(err)
	}

	tp, _, err := client.ReadFrame()
	if err != nil {
		t.Fatal(err)
	}
	if tp != closeFrame {
		t.Errorf("expected close frame, got %s", tp)
	}

	err = client.Close()
	if err != nil {
		t.Error(err)
	}

	for err := range errorsInServer {
		t.Error("server: " + err)
	}
}

func TestReceiveWrongType(t *testing.T) {
	defer goleak.VerifyNone(t)

	errorsInServer := make(chan string, 10)
	handler := func(conn *Conn) {
		// server code
		buf := make([]byte, 128)

		n, err := conn.ReceiveBinary(buf)
		if err != ErrConnClosed || n != 0 {
			errorsInServer <- fmt.Sprintf("wrong type: buf=[% x], err=%s", buf[:n], err)
		}

		err = conn.Close(StatusOK, "OK")
		if err != nil {
			errorsInServer <- err.Error()
		}

		close(errorsInServer)
	}

	server, err := StartTestServer(handler)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	// fake client
	client, err := server.Connect()
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	// send a text frame
	err = client.SendFrame(Text, []byte{65}, true)
	if err != nil {
		t.Fatal(err)
	}

	tp, _, err := client.ReadFrame()
	if err != nil {
		t.Fatal(err)
	}
	if tp != closeFrame {
		t.Errorf("expected close frame, got %s", tp)
	}

	err = client.Close()
	if err != nil {
		t.Error(err)
	}

	for err := range errorsInServer {
		t.Error("server: " + err)
	}
}

// TestInvalidCont tests the case where a continuation frame does not
// have the correct opcode set.
func TestInvalidCont(t *testing.T) {
	defer goleak.VerifyNone(t)

	errorsInServer := make(chan string, 10)
	handler := func(conn *Conn) {
		// server code
		s, err := conn.ReceiveText(128)
		if err != nil || s != "firstsecond" {
			errorsInServer <- fmt.Sprintf("ReceiveText: %q, %s", s, err)
		}

		s, err = conn.ReceiveText(128)
		if err != ErrConnClosed || s != "" {
			errorsInServer <- fmt.Sprintf("ReceiveText: %q, %s", s, err)
		}

		err = conn.Close(StatusOK, "OK")
		if err != ErrConnClosed {
			errorsInServer <- fmt.Sprintf("Close: %s", err)
		}

		close(errorsInServer)
	}

	server, err := StartTestServer(handler)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	// fake client
	client, err := server.Connect()
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	// send a good text message
	err = client.SendFrame(Text, []byte("first"), false)
	if err != nil {
		t.Fatal(err)
	}
	err = client.SendFrame(contFrame, []byte("second"), true)
	if err != nil {
		t.Fatal(err)
	}

	// send an invalid text message
	err = client.SendFrame(Text, []byte("first"), false)
	if err != nil {
		t.Fatal(err)
	}
	err = client.SendFrame(Text, []byte("second"), true)
	if err != nil {
		t.Fatal(err)
	}

	tp, msg, err := client.ReadFrame()
	if err != nil {
		t.Fatal(err)
	}
	if tp != closeFrame {
		t.Errorf("expected close frame, got %s", tp)
	}
	if !bytes.Equal(msg, []byte{1002 / 256, 1002 % 256}) {
		t.Errorf("expected close code 1002, got %d", 256*Status(msg[0])+Status(msg[1]))
	}

	err = client.Close()
	if err != nil {
		t.Error(err)
	}

	for err := range errorsInServer {
		t.Error("server: " + err)
	}
}

// TestTooLong tests that too long messages are correctly processed
// on the server side.
func TestTooLong(t *testing.T) {
	var serverError string
	server, err := StartTestServer(func(conn *Conn) {
		// We send messages of 300 bytes length, but only provide a buffer of
		// 150 bytes.  Make sure ErrTooLarge is reported.
		buf := make([]byte, 150)
		status := StatusOK
		for {
			n, err := conn.ReceiveBinary(buf)
			if err == ErrConnClosed {
				return
			} else if err != ErrTooLarge {
				serverError = "errTooLarge not reported"
				status = StatusProtocolError
				break
			}
			err = conn.SendBinary(buf[:n])
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
