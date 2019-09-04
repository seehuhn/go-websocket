package websocket

import (
	"log"
	"net/http"
)

const websocketGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

type Handler struct {
	AccessOk func(conn *Conn, protocols []string) bool
	Handle   func(conn *Conn)
}

func (handler *Handler) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hijacking failed", http.StatusInternalServerError)
		return
	}

	conn := newConn()
	// TODO(voss): move the handshake into newConn()
	status, msg := conn.handshake(w, req, handler.AccessOk)
	if status == http.StatusSwitchingProtocols {
		w.WriteHeader(status)
	} else {
		http.Error(w, msg, status)
		return
	}

	raw, rw, err := hijacker.Hijack()
	if err != nil {
		panic("Hijack failed: " + err.Error())
	}

	defer raw.Close()
	conn.conn = raw
	conn.rw = rw

	serverReady := make(chan struct{})
	userHandlerDone := make(chan struct{})
	go func() {
		<-serverReady

		// run the user-provided handler
		handler.Handle(conn)

		log.Println("user handler done")
		close(userHandlerDone)
	}()

	conn.handle(serverReady, userHandlerDone)
}

type ioResult struct {
	N   int
	Err error
}
