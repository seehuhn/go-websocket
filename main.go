// +build ignore

package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

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
		time.Sleep(time.Second)
		err := conn.SendText("hello, client!")
		if err != nil {
			log.Fatal("A ", err)
		}

		msg, err := conn.ReceiveText(128)
		if err != nil {
			log.Fatal("B ", err)
		}
		fmt.Println("message:", msg)

		if msg == "Noptaloo" {
			break
		}
	}
	conn.Close(4444, "done")
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
