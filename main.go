// +build ignore

package main

import (
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"

	"seehuhn.de/go/websocket"
)

var port = flag.String("port", "8080", "Define what TCP port to bind to")
var root = flag.String("root", "www", "Define the root filesystem path")

func checkAccess(conn *websocket.Conn, protocols []string) bool {
	log.Println("ResourceName:", conn.ResourceName)
	log.Println("Origin:", conn.Origin)
	log.Println("Protocols:", strings.Join(protocols, ", "))
	conn.Protocol = protocols[0]
	return true
}

func handle(conn *websocket.Conn) websocket.CloseCode {
	for {
		err := conn.SendText("hello, client!")
		if err != nil {
			log.Fatal("A ", err)
		}

		_, r, err := conn.ReceiveMessage()
		if err != nil {
			log.Fatal("B ", err)
		}
		buf := make([]byte, 256)
		n, err := r.Read(buf)
		if err != nil && err != io.EOF {
			log.Fatal("C ", err)
		}
		msg := string(buf[:n])
		fmt.Println("message:", msg)

		if msg == "Noptaloo" {
			return 4444
		}
	}
	return websocket.CodeOK
}

func main() {
	http.Handle("/", http.FileServer(http.Dir(*root)))

	websocket := &websocket.Handler{
		AccessOk: checkAccess,
		Handle:   handle,
	}
	http.Handle("/test", websocket)

	listenAddr := ":" + *port
	log.Println("listening at", listenAddr)
	log.Println("serving directory", *root)
	http.ListenAndServe(listenAddr, nil)
}
