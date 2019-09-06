package websocket

import (
	"fmt"
	"unicode/utf8"
)

func formatBody(body []byte) string {
	isString := true
	var runes []rune
	pos := 0
	for i := 0; i < 60 && pos < len(body); i++ {
		r, n := utf8.DecodeRune(body[pos:])
		if r == utf8.RuneError {
			isString = false
			break
		}
		runes = append(runes, r)
		pos += n
	}

	if isString {
		return fmt.Sprintf("%q", string(runes))
	}

	n := len(body)
	m := 20
	extra := ""
	if m >= n {
		m = n
	} else {
		extra = " ..."
	}
	return fmt.Sprintf("[% 02x%s]", body[:m], extra)
}
