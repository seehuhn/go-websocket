package main

import (
	"flag"
	"log"
	"net/http"

	"seehuhn.de/go/websocket"
)

//go:generate esc -o fs.go -prefix www www

var listenAddr = flag.String("port", ":8080", "the address to listen on")
var serveFiles = flag.Bool("serve-files", false,
	"serve file from the file system instead of built-in")

func main() {
	flag.Parse()

	http.Handle("/", http.FileServer(FS(*serveFiles)))
	chat := NewChat()
	websocketHandler := &websocket.Handler{
		Handle: func(conn *websocket.Conn) { chat.Add(conn) },
	}
	http.Handle("/api/chat", websocketHandler)

	log.Println("listening on", *listenAddr)
	log.Fatal(http.ListenAndServe(*listenAddr, nil))
}
