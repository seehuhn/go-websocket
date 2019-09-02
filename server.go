package websocket

import (
	"context"
	"log"
	"net/http"
)

const websocketGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"

type Handler struct {
	AccessOk func(conn *Conn, protocols []string) bool
	Handle   func(ctx context.Context, conn *Conn)
}

func (handler *Handler) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	log.Println("Headers:")
	for header, hh := range req.Header {
		for _, h := range hh {
			log.Println("-", header+":", h)
		}
	}
	log.Println()

	hijacker, ok := w.(http.Hijacker)
	if !ok {
		http.Error(w, "hijacking failed", http.StatusInternalServerError)
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

	raw, buf, err := hijacker.Hijack()
	if err != nil {
		panic("Hijack failed: " + err.Error())
	}
	defer raw.Close()
	conn.conn = raw
	conn.rw = buf

	handlerDone := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	go func(ctx context.Context) {
		handler.Handle(ctx, conn)
		close(handlerDone)
	}(ctx)

	conn.handle(handlerDone, cancel)
}
