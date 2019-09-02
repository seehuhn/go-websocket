// +build ignore

package main

import (
	"context"
	"flag"
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

func handle(ctx context.Context, conn *websocket.Conn) {
	log.Println("start")
}

func main() {
	websocket := &websocket.Handler{
		AccessOk: checkAccess,
		Handle:   handle,
	}

	http.Handle("/", http.FileServer(http.Dir(*root)))
	http.Handle("/test", websocket)

	listenAddr := ":" + *port
	log.Println("listening at", listenAddr)
	log.Println("serving directory", *root)
	http.ListenAndServe(listenAddr, nil)
}
