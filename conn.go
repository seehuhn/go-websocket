package websocket

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
	"log"
	"net"
	"net/http"
	"net/url"
	"strings"
)

type Conn struct {
	ResourceName *url.URL
	Origin       *url.URL
	Protocol     string

	conn net.Conn
	rw   *bufio.ReadWriter
}

func (wsc *Conn) handshake(w http.ResponseWriter, req *http.Request,
	accessOk func(*Conn, []string) bool) (status int, message string) {

	headers := w.Header()

	version := req.Header.Get("Sec-Websocket-Version")
	if version != "13" {
		headers.Set("Sec-WebSocket-Version", "13")
		return http.StatusUpgradeRequired, "unknown version"
	}
	if strings.ToLower(req.Header.Get("Upgrade")) != "websocket" {
		return http.StatusBadRequest, "missing upgrade header"
	}
	connection := strings.ToLower(req.Header.Get("Connection"))
	if !strings.Contains(connection, "upgrade") {
		return http.StatusBadRequest, "missing connection header"
	}
	key := req.Header.Get("Sec-Websocket-Key")
	if key == "" {
		return http.StatusBadRequest, "missing Sec-Websocket-Key"
	}

	var scheme string
	if req.TLS != nil {
		scheme = "wss"
	} else {
		scheme = "ws"
	}
	resourceName, err := url.ParseRequestURI(scheme + "://" + req.Host + req.URL.RequestURI())
	if err != nil {
		return http.StatusBadRequest, "invalid Request-URI"
	}
	wsc.ResourceName = resourceName

	var origin *url.URL
	originString := req.Header.Get("Origin")
	if originString != "" {
		origin, err = url.ParseRequestURI(originString)
		if err != nil {
			return http.StatusBadRequest, "invalid Origin"
		}
	}
	wsc.Origin = origin

	var protocols []string
	protocol := strings.TrimSpace(req.Header.Get("Sec-Websocket-Protocol"))
	if protocol != "" {
		pp := strings.Split(protocol, ",")
		for i := 0; i < len(pp); i++ {
			protocols = append(protocols, strings.TrimSpace(pp[i]))
		}
	}

	ok := accessOk(wsc, protocols)
	if !ok {
		return http.StatusForbidden, "not allowed"
	}

	if wsc.Protocol != "" {
		headers.Set("Sec-WebSocket-Protocol", wsc.Protocol)
	}
	headers.Set("Upgrade", "websocket")
	headers.Set("Connection", "Upgrade")
	headers.Set("Sec-WebSocket-Accept", getAccept(key))
	return http.StatusSwitchingProtocols, ""
}

func (conn *Conn) handle(done <-chan struct{}, cancel func()) {
	rcv := make(chan *frame)
	go func(rcv chan<- *frame) {
		conn.readFrames(rcv)
	}(rcv)

	isOpen := true
	for isOpen {
		select {
		case frame, ok := <-rcv:
			if !ok {
				rcv = nil
				continue
			}
			log.Println(frame)
		case _, ok := <-done:
			if !ok {
				done = nil
			}
		}
	}
	log.Println("done")
}

func (conn *Conn) readFrames(frames chan<- *frame) {
	buf := make([]byte, 8)
	for {
		n, _ := conn.rw.Read(buf[:2])
		if n < 2 {
			break
		}

		fin := buf[0] & 1
		reserved := (buf[0] >> 1) & 7
		if reserved != 0 {
			break
		}
		opcode := (buf[0] >> 4) & 15

		mask := buf[1] & 1
		if mask == 0 {
			break
		}

		// read the length
		l8 := (buf[1] >> 1) & 127
		lengthBytes := 1
		if l8 == 127 {
			lengthBytes = 8
		} else if l8 == 126 {
			lengthBytes = 2
		}
		if lengthBytes > 1 {
			n, _ := conn.rw.Read(buf[:lengthBytes])
			if n < lengthBytes {
				break
			}
		} else {
			buf[0] = l8
		}
		var length uint64
		for i := 0; i < lengthBytes; i++ {
			length = length<<8 | uint64(buf[i])
		}

		// read the masking key
		n, _ = conn.rw.Read(buf[:4])
		if n < 4 {
			break
		}

		log.Println("read some:", fin, opcode, length, buf[:4])
	}
	close(frames)
}

type opcodeType byte

const (
	opcodeCont opcodeType = iota
	opcodeText
	opcodeBinary
	opcodeReserved3
	opcodeReserved4
	opcodeReserved5
	opcodeReserved6
	opcodeReserved7
	opcodeClose
	opcodePing
	opcodePong
	opcodeReservedB
	opcodeReservedC
	opcodeReservedD
	opcodeReservedE
	opcodeReservedF
)

type frame struct {
	Fin    bool
	Rsv    byte
	Opcode opcodeType
	Masked bool
	Len    uint64
	Mask   []byte
}

func getAccept(key string) string {
	h := sha1.New()
	h.Write([]byte(key))
	h.Write([]byte(websocketGUID))
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}
