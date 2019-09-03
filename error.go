package websocket

type webSocketError string

func (err webSocketError) Error() string {
	return string(err)
}

const (
	ErrConnClosed = webSocketError("connection closed")

	errFrameFormat = webSocketError("invalid frame format")
	errFrameOpcode = webSocketError("invalid frame opcode")
	errFrameType   = webSocketError("invalid frame type")
)
