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
	"testing"
)

func TestReadBinary(t *testing.T) {
	wait := make(chan struct{})
	handler := func(conn *Conn) {
		buf := make([]byte, 2)

		n, err := conn.ReceiveBinary(buf)
		if err != nil || n != 1 || buf[0] != 1 {
			t.Fatalf("read 1 failed: buf=[%x], err=%s", buf[:n], err)
		}

		n, err = conn.ReceiveBinary(buf)
		if err != ErrMessageType || n != 0 {
			t.Fatalf("read 2 failed: buf=[%x], err=%s", buf[:n], err)
		}

		n, err = conn.ReceiveBinary(buf)
		if err != nil || n != 1 || buf[0] != 3 {
			t.Fatalf("read 3 failed: buf=[%x], err=%s", buf[:n], err)
		}

		n, err = conn.ReceiveBinary(buf)
		if err != ErrTooLarge || n != 2 || buf[0] != 4 {
			t.Fatalf("read 4 failed: buf=[%x], err=%s", buf[:n], err)
		}

		n, err = conn.ReceiveBinary(buf)
		if err != nil || n != 1 || buf[0] != 5 {
			t.Fatalf("read 5 failed: buf=[%x], err=%s", buf[:n], err)
		}

		n, err = conn.ReceiveBinary(buf)
		if err != ErrConnClosed || n != 0 {
			t.Fatalf("not properly closed: buf=[%x], err=%s", buf[:n], err)
		}

		conn.Close(StatusOK, "OK")
		close(wait)
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
	defer client.Close()

	// send one byte
	err = client.SendFrame(Binary, []byte{1})
	if err != nil {
		t.Fatal(err)
	}

	// wrong message type, in several packets - should be discarded
	tmp := make([]byte, 128)
	var tp = Text
	for i := 0; i < 10; i++ {
		err = client.SendNonsenseFrame(tmp, tp, 100, false)
		if err != nil {
			t.Fatal(err)
		}
		tp = contFrame
	}
	err = client.SendNonsenseFrame(tmp, tp, 29, true)
	if err != nil {
		t.Fatal(err)
	}

	// send one byte
	err = client.SendFrame(Binary, []byte{3})
	if err != nil {
		t.Fatal(err)
	}

	// too long message
	err = client.SendFrame(Binary, []byte{4, 4, 4, 4})
	if err != nil {
		t.Fatal(err)
	}

	// send one byte
	err = client.SendFrame(Binary, []byte{5})
	if err != nil {
		t.Error(err)
	}

	err = client.Close()
	if err != nil {
		t.Error(err)
	}

	<-wait
}
