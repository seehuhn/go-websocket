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
	"embed"
	"flag"
	"io/fs"
	"log"
	"net/http"

	"seehuhn.de/go/websocket"
)

//go:embed www/**
var www embed.FS

var listenAddr = flag.String("port", ":8080", "the address to listen on")

func main() {
	flag.Parse()

	staticFiles, err := fs.Sub(www, "www")
	if err != nil {
		log.Fatal(err)
	}

	http.Handle("/", http.FileServer(http.FS(staticFiles)))
	chat := NewChat()
	websocketHandler := &websocket.Handler{
		Handle: func(conn *websocket.Conn) { chat.Add(conn) },
	}
	http.Handle("/api/chat", websocketHandler)

	log.Println("listening on", *listenAddr)
	log.Fatal(http.ListenAndServe(*listenAddr, nil))
}
