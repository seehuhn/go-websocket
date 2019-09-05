package websocket

type webSocketError string

func (err webSocketError) Error() string {
	return string(err)
}

const (
	ErrConnClosed  = webSocketError("connection closed")
	ErrMessageType = webSocketError("wrong message type")
	ErrStatusCode  = webSocketError("invalid status code")
	ErrTooLarge    = webSocketError("message too large")

	errFrameFormat = webSocketError("invalid frame format")
	errFrameOpcode = webSocketError("invalid frame opcode")
)
