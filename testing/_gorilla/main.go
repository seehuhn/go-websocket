package main

import (
	"bytes"
	"crypto/rand"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	"os"
	"time"

	gorilla "github.com/gorilla/websocket"
	"seehuhn.de/go/websocket"
)

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

func runEchoServer(shutdown <-chan struct{}, status chan<- string) {
	// open the socket
	l, err := net.Listen("tcp", "")
	if err != nil {
		log.Fatal(err)
	}

	status <- l.Addr().String()

	// start the websocket server
	go func() {
		websocket := &websocket.Handler{
			Handle: echo,
		}
		http.Serve(l, websocket)
		log.Println("server terminated")
		close(status)
	}()

	<-shutdown
	l.Close()
}

func runTestingClient(url string) {
	log.Println("connecting to", url)
	dialer := &gorilla.Dialer{}
	conn, _, err := dialer.Dial(url, nil)
	if err != nil {
		log.Println("connection failed:", err)
		return
	}
	defer func() {
		err := conn.Close()
		if err != nil {
			log.Println("close error:", err)
		}
		log.Println("client terminated")
	}()

	data, err := os.Create("x.dat")
	if err != nil {
		log.Println("cannot open log file:", err)
		return
	}
	defer data.Close()

	N := 1000
	for l := 0; l <= 1e6; l += 512 {
		msg := make([]byte, l)
		_, err = io.ReadFull(rand.Reader, msg)
		if err != nil {
			log.Println("rand failed:", err)
			return
		}
		origMsg := msg

		t0 := time.Now()
		tp := gorilla.BinaryMessage
		for i := 0; i < N; i++ {
			err = conn.WriteMessage(tp, msg)
			if err != nil {
				log.Println("write error:", err)
				return
			}
			tp, msg, err = conn.ReadMessage()
			if err != nil {
				log.Println("read error:", err)
				return
			}
		}
		t1 := time.Now()
		if tp != gorilla.BinaryMessage || bytes.Compare(msg, origMsg) != 0 {
			log.Println("corruption detected")
		}

		// average round trip time in ms
		dt := t1.Sub(t0).Seconds() / float64(N) * 1000
		fmt.Printf("%6d %8.3fms\n", l, dt)
		fmt.Fprintln(data, l, dt)
		data.Sync()
	}
}

func main() {
	flag.Parse()

	shutdown := make(chan struct{})
	serverStatus := make(chan string)
	go runEchoServer(shutdown, serverStatus)

	listenAddr := <-serverStatus
	runTestingClient("ws://" + listenAddr + "/")

	close(shutdown)
	<-serverStatus
}
