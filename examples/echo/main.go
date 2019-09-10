package main

import (
	"flag"
	"io"
	"io/ioutil"
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
			io.Copy(ioutil.Discard, r)
			break
		}

		_, err = io.Copy(w, r)
		if err != nil {
			log.Println("write error:", err)
			io.Copy(ioutil.Discard, r)
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
