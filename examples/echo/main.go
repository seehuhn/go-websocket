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

package main

import (
	"flag"
	"io"
	"log"
	"net/http"

	"seehuhn.de/go/websocket"
)

var port = flag.String("port", "8080", "what TCP port to bind to")

func echo(conn *websocket.Conn) {
	defer conn.Close(websocket.StatusOK, "")

	for {
		tp, r, err := conn.ReceiveMessage()
		if err == websocket.ErrConnClosed {
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
			io.Copy(io.Discard, r)
			break
		}

		_, err = io.Copy(w, r)
		if err != nil {
			log.Println("write error:", err)
			io.Copy(io.Discard, r)
		}

		err = w.Close()
		if err != nil && err != websocket.ErrConnClosed {
			log.Println("close error:", err)
		}
	}
}

func main() {
	flag.Parse()

	websocketHandler := &websocket.Handler{
		Handle: echo,
	}
	http.Handle("/", websocketHandler)

	listenAddr := ":" + *port
	log.Println("listening at", listenAddr)
	log.Fatal(http.ListenAndServe(listenAddr, nil))
}
