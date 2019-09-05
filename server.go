package websocket

import (
	"log"
	"net/http"
)

type Handler struct {
	AccessOk func(conn *Conn, protocols []string) bool
	Handle   func(conn *Conn)
}

func (handler *Handler) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hijacking not supported",
			http.StatusInternalServerError)
		return
	}

	conn := &Conn{}
	status, msg := conn.handshake(w, req, handler.AccessOk)
	if status == http.StatusSwitchingProtocols {
		w.WriteHeader(status)
	} else {
		http.Error(w, msg, status)
		return
	}
	raw, rw, err := hijacker.Hijack()
	if err != nil {
		http.Error(w, "hijacking failed", http.StatusInternalServerError)
		return
	}
	conn.raw = raw
	conn.rw = rw

	// TODO(voss): should the rest of this function be moved into a goroutine
	// and the ServeHTTP method allowed to finish early?

	log.Println("opening new connection")

	// start the server go-routines
	writerReady := make(chan struct{})
	go conn.writeMultiplexer(writerReady)
	<-writerReady
	readerReady := make(chan struct{})
	readerDone := make(chan struct{})
	conn.readerDone = readerDone
	go func() {
		conn.readMultiplexer(readerReady)
		close(readerDone)
	}()
	<-readerReady

	handler.Handle(conn)
}
