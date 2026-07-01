// seehuhn.de/go/websocket - an http server to establish websocket connections
// Copyright (C) 2026  Jochen Voss <voss@seehuhn.de>
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
	"io"
	"net"
	"sync"
	"testing"
)

func appendHeader(buf []byte, op MessageType, l int, final bool) []byte {
	b0 := byte(op)
	if final {
		b0 |= 128
	}
	buf = append(buf, b0)

	if l < 126 {
		buf = append(buf, byte(l)|128)
	} else if l < (1 << 16) {
		buf = append(buf, 126|128, byte(l>>8), byte(l))
	} else {
		buf = append(buf,
			127|128,
			byte(l>>56), byte(l>>48), byte(l>>40), byte(l>>32),
			byte(l>>24), byte(l>>16), byte(l>>8), byte(l))
	}
	buf = append(buf, 0, 0, 0, 0) // we use the zero mask
	return buf
}

func appendFrame(buf []byte, op MessageType, data []byte, final bool) []byte {
	buf = appendHeader(buf, op, len(data), final)
	buf = append(buf, data...)
	return buf
}

// FuzzReader tries to make sure that the reader never hangs.
func FuzzReader(f *testing.F) {
	var buf []byte
	buf = appendFrame(buf, closeFrame, nil, true)
	f.Add(buf)

	buf = buf[:0]
	buf = appendFrame(buf, Text, []byte("some text"), true)
	buf = appendFrame(buf, closeFrame, nil, true)
	f.Add(buf)

	buf = buf[:0]
	buf = appendFrame(buf, Binary, []byte{1, 2, 3}, true)
	buf = appendFrame(buf, closeFrame, []byte{1000 / 256, 1000 % 256, 65, 66, 67}, true)
	f.Add(buf)

	buf = buf[:0]
	buf = appendFrame(buf, Text, []byte("Incomprehen"), false)
	buf = appendFrame(buf, Text, []byte("sibility"), true)
	buf = appendFrame(buf, closeFrame, []byte{1000 / 256, 1000 % 256, 65, 66, 67}, true)
	f.Add(buf)

	f.Add([]byte{0x88, 0x80, 0x01, 0x02, 0x03, 0x04})
	f.Fuzz(func(t *testing.T, data []byte) {
		client, server := net.Pipe()
		rw := bufio.NewReadWriter(bufio.NewReader(server), bufio.NewWriter(server))

		conn := &Conn{}
		conn.initialize(server, rw)

		wg := &sync.WaitGroup{}

		wg.Go(func() {
			for {
				tp, r, err := conn.ReceiveMessage()
				if err != nil {
					break
				}

				w, err := conn.SendMessage(tp)
				if err != nil {
					io.Copy(io.Discard, r)
					break
				}

				_, err = io.Copy(w, r)
				if err != nil {
					io.Copy(io.Discard, r)
				}

				w.Close()
			}
			conn.Close(StatusOK, "")
		})

		wg.Go(func() {
			io.Copy(io.Discard, client)
		})

		wg.Go(func() {
			client.Write(data)
			client.Close()
		})

		conn.Wait()
		wg.Wait()
	})
}
