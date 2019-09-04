// +build ignore

package main

import (
	"flag"
	"fmt"
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

func handle(conn *websocket.Conn) {
	for {
		err := conn.SendText("hello, client!")
		if err != nil {
			log.Fatal(err)
		}

		opcode, r, err := conn.ReceiveMessage()
		if err != nil {
			log.Fatal(err)
		}
		fmt.Println("received message type", opcode)
		buf := make([]byte, 256)
		n, err := r.Read(buf)
		if err != nil {
			log.Fatal(err)
		}
		fmt.Println("message:", string(buf[:n]))
	}
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
