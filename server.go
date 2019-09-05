package websocket

import (
	"log"
	"net/http"
	"time"
)

type Handler struct {
	AccessOk func(conn *Conn, protocols []string) bool
	Handle   func(conn *Conn) CloseCode
}

func (handler *Handler) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hijacking failed", http.StatusInternalServerError)
		return
	}

	conn := newConn()
	// TODO(voss): move the handshake into newConn()?
	status, msg := conn.handshake(w, req, handler.AccessOk)
	if status == http.StatusSwitchingProtocols {
		w.WriteHeader(status)
	} else {
		http.Error(w, msg, status)
		return
	}
	// TODO(voss): move this into newConn(), too?
	raw, rw, err := hijacker.Hijack()
	if err != nil {
		panic("Hijack failed: " + err.Error())
	}
	conn.conn = raw
	conn.rw = rw

	// TODO(voss): should the rest of this function be moved in a goroutine
	// and the ServeHTTP method allowed to finish early?

	log.Println("opening new connection")

	// start the server go-routines
	writerReady := make(chan struct{})
	go conn.writeMultiplexer(writerReady)
	<-writerReady
	readerReady := make(chan struct{})
	readerDone := make(chan struct{})
	go func() {
		conn.readMultiplexer(readerReady)
		close(readerDone)
	}()
	<-readerReady

	// ----------------------------------------------------------------------
	log.Println("starting user handler")
	code := handler.Handle(conn)
	log.Println("user handler done")
	// ----------------------------------------------------------------------

	// send a close frame and wait for confirmation from the client
	conn.sendCloseFrame(code, "")
	needsClose := true
	timeOut := time.NewTimer(3 * time.Second)
	select {
	case <-readerDone:
		if !timeOut.Stop() {
			<-timeOut.C
		}
	case <-timeOut.C:
		log.Println("client did not send close frame, killing connection")
		raw.Close()
		needsClose = false
	}

	// The reader uses conn.sendControlFrame, so we must be sure the
	// reader has stopped, before we can close the challer to stop the
	// writer.
	<-readerDone
	close(conn.sendControlFrame)

	log.Println("closing connection")
	if needsClose {
		raw.Close()
	}
}

type ioResult struct {
	N   int
	Err error
}
