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
	"time"
)

// Conn represents a websocket connection initiated by a client.  All
// fields are read-only.  It is ok to access a Conn from different
// goroutines concurrently.  The connection must be closed using the
// Close() method after use, to free all allocated resources.
type Conn struct {
	ResourceName *url.URL
	Origin       *url.URL
	Protocol     string

	raw net.Conn
	rw  *bufio.ReadWriter

	readerDone <-chan struct{}

	getDataReader    <-chan *frameReader
	getDataWriter    <-chan *frameWriter
	sendControlFrame chan<- *frame
}

type MessageType byte

const (
	Text   MessageType = 1
	Binary MessageType = 2

	contFrame  MessageType = 0
	closeFrame MessageType = 8
	pingFrame  MessageType = 9
	pongFrame  MessageType = 10
)

type Status uint16

// Websocket status codes as defined in RFC 6455
// See: https://tools.ietf.org/html/rfc6455#section-7.4.1
const (
	StatusOK                  Status = 1000
	StatusGoingAway           Status = 1001
	StatusProtocolError       Status = 1002
	StatusUnsupportedType     Status = 1003
	StatusInvalidData         Status = 1007
	StatusPolicyViolation     Status = 1008
	StatusTooBig              Status = 1009
	StatusMissingExtension    Status = 1010
	StatusInternalServerError Status = 1011

	statusMissing Status = 1005
)

const (
	websocketGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11" // from RFC 6455
)

type header struct {
	Length uint64
	Mask   []byte
	Final  bool
	Opcode MessageType
}

type frame struct {
	Body   []byte
	Final  bool
	Opcode MessageType
}

func (conn *Conn) handshake(w http.ResponseWriter, req *http.Request,
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
	conn.ResourceName = resourceName

	var origin *url.URL
	originString := req.Header.Get("Origin")
	if originString != "" {
		origin, err = url.ParseRequestURI(originString)
		if err != nil {
			return http.StatusBadRequest, "invalid Origin"
		}
	}
	conn.Origin = origin

	var protocols []string
	protocol := strings.TrimSpace(req.Header.Get("Sec-Websocket-Protocol"))
	if protocol != "" {
		pp := strings.Split(protocol, ",")
		for i := 0; i < len(pp); i++ {
			protocols = append(protocols, strings.TrimSpace(pp[i]))
		}
	}

	if accessOk != nil {
		ok := accessOk(conn, protocols)
		if !ok {
			return http.StatusForbidden, "not allowed"
		}
	}

	h := sha1.New()
	h.Write([]byte(key))
	h.Write([]byte(websocketGUID))
	accept := base64.StdEncoding.EncodeToString(h.Sum(nil))

	if conn.Protocol != "" {
		headers.Set("Sec-WebSocket-Protocol", conn.Protocol)
	}
	headers.Set("Upgrade", "websocket")
	headers.Set("Connection", "Upgrade")
	headers.Set("Sec-WebSocket-Accept", accept)
	return http.StatusSwitchingProtocols, ""
}

func (conn *Conn) sendCloseFrame(status Status, body []byte) {
	var buf []byte
	if status != 0 && status != statusMissing {
		buf = make([]byte, 2+len(body))
		buf[0] = byte(status >> 8)
		buf[1] = byte(status)
		copy(buf[2:], body)
	}
	ctl := &frame{
		Opcode: closeFrame,
		Body:   buf,
		Final:  true,
	}
	conn.sendControlFrame <- ctl
}

func (conn *Conn) Close(code Status, message string) error {
	if code < 1000 || code >= 5000 {
		return ErrStatusCode
	}

	body := []byte(message)
	if len(body) > 125-2 {
		return ErrTooLarge
	}

	conn.sendCloseFrame(code, body)
	needsClose := true
	timeOut := time.NewTimer(3 * time.Second)
	select {
	case <-conn.readerDone:
		if !timeOut.Stop() {
			<-timeOut.C
		}
	case <-timeOut.C:
		log.Println("client did not send close frame, killing connection")
		conn.raw.Close()
		needsClose = false
	}

	// The reader uses conn.sendControlFrame, so we must be sure the
	// reader has stopped, before we can close the challer to stop the
	// writer.
	<-conn.readerDone
	close(conn.sendControlFrame)

	log.Println("closing connection")
	if needsClose {
		conn.raw.Close()
	}

	return nil
}
