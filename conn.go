package websocket

import (
	"bufio"
	"crypto/sha1"
	"encoding/base64"
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

	getDataReader    <-chan *frameReader
	getDataWriter    <-chan *frameWriter
	sendControlFrame chan<- *frame
}

type FrameType byte

const (
	contFrame   FrameType = 0
	TextFrame   FrameType = 1
	BinaryFrame FrameType = 2
	closeFrame  FrameType = 8
	pingFrame   FrameType = 9
	pongFrame   FrameType = 10
)

type CloseCode uint16

const (
	CodeOK                  CloseCode = 1000
	codeProtocolError       CloseCode = 1002
	codeMissing             CloseCode = 1005
	codeUnexpectedCondition CloseCode = 1011
)

const (
	websocketGUID = "258EAFA5-E914-47DA-95CA-C5AB0DC85B11" // from RFC 6455
	pingGUID      = "9c5192f2-4504-4cda-b9d4-8ec8e744166a" // locally generated
)

type header struct {
	Length uint64
	Mask   []byte
	Final  bool
	Opcode FrameType
}

type frame struct {
	Body   []byte
	Final  bool
	Opcode FrameType
}

func newConn() *Conn {
	return &Conn{}
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

func getAccept(key string) string {
	h := sha1.New()
	h.Write([]byte(key))
	h.Write([]byte(websocketGUID))
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}

func (conn *Conn) sendCloseFrame(code CloseCode, message string) {
	msg := []byte(message)
	var buf []byte
	if code != 0 && code != codeMissing {
		buf = make([]byte, 2+len(msg))
		buf[0] = byte(code >> 8)
		buf[1] = byte(code)
		copy(buf[2:], msg)
	}
	ctl := &frame{
		Opcode: closeFrame,
		Body:   buf,
		Final:  true,
	}
	conn.sendControlFrame <- ctl
}
