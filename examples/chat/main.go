package main

import (
	"flag"
	"log"
	"net/http"

	"seehuhn.de/go/websocket"
)

var port = flag.String("port", "8080", "Define what TCP port to bind to")
var root = flag.String("root", "www", "Define the root filesystem path")

func main() {
	flag.Parse()

	chat := NewChat()

	websocketHandler := &websocket.Handler{
		Handle: func(conn *websocket.Conn) { chat.Add(conn) },
	}
	http.Handle("/api/chat", websocketHandler)

	http.Handle("/", http.FileServer(http.Dir(*root)))
	log.Println("serving directory", *root)

	listenAddr := ":" + *port
	log.Println("listening at", listenAddr)
	log.Fatal(http.ListenAndServe(listenAddr, nil))
}
