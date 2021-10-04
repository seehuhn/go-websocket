// seehuhn.de/go/websocket - an http server to establish websocket connections
// Copyright (C) 2019  Jochen Voss <voss@seehuhn.de>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU General Public License for more details.
//
// You should have received a copy of the GNU General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"seehuhn.de/go/websocket"
)

// Chat represent one chat server.
type Chat struct {
	send chan<- *Message

	sync.Mutex
	change  *sync.Cond
	members *members
}

// NewChat creates a new Chat objects and starts the associated goroutines.
func NewChat() *Chat {
	c := make(chan *Message, 1)
	chat := &Chat{
		send:    c,
		members: &members{},
	}
	chat.change = sync.NewCond(&chat.Mutex)

	go chat.broadcastMessages(c)
	return chat
}

func (chat *Chat) receiveMessages(ctl <-chan struct{}) {
	c := make(chan *members, 1)
	currentMembers := make(chan *members, 1)

	go func() {
		chat.Lock()
		for {
			chat.change.Wait()
			next := chat.members.Copy()
			c <- next
		}
		chat.Unlock()
		close(c)
	}()

	currentMembers <- <-c
	for members := range currentMembers {
		ctx, abortReceive := context.WithCancel(context.Background())
		go func() {
			// When an updated members list is available, abort the current read.
			next := <-c
			abortReceive()
			currentMembers <- next
		}()
		for {
			idx, msgText, err := websocket.ReceiveOneText(ctx, 1024, members.conns)
			if idx < 0 {
				// updated members list
				break
			}

			conn := members.conns[idx]
			name := members.names[idx]
			switch {
			case msgText == "/quit":
				err := conn.Close(websocket.StatusOK, "quit request received")
				if err != nil {
					log.Println("close error:", err)
				}
				fallthrough
			case err != nil:
				chat.Remove(conn)
				chat.send <- &Message{
					When: time.Now(),
					Text: fmt.Sprintf("%q has left this chat", name),
				}
			case msgText == "/names":
				chat.send <- &Message{
					When: time.Now(),
					Text: "members: " + strings.Join(members.names, ", "),
				}
			default:
				chat.send <- &Message{
					When: time.Now(),
					From: name,
					Text: msgText,
				}
			}
		}
	}
}

func (chat *Chat) broadcastMessages(messages <-chan *Message) {
	for msg := range messages {
		msgJSON, err := msg.asJSON()
		if err != nil {
			log.Println("JSON encoding failed:", err)
			continue
		}

		chat.Lock()
		var closed []int
		for i, conn := range chat.members.conns {
			err = conn.SendText(msgJSON)
			if err == websocket.ErrConnClosed {
				closed = append(closed, i)
			} else if err != nil {
				log.Printf("sending failed for %q: %s\n", chat.members.names[i], err)
			}
		}
		var needsCloseConn []*websocket.Conn
		var needsCloseName []string
		if len(closed) > 0 {
			members := chat.members
			n := len(members.conns)
			for i := len(closed) - 1; i >= 0; i-- {
				idx := closed[i]
				needsCloseConn = append(needsCloseConn, members.conns[idx])
				needsCloseName = append(needsCloseName, members.names[idx])

				n--
				if idx < n {
					members.conns[idx] = members.conns[n]
					members.names[idx] = members.names[n]
				}
			}
			members.conns = members.conns[:n]
			members.names = members.names[:n]
			chat.change.Broadcast()
		}
		chat.Unlock()

		for i, conn := range needsCloseConn {
			conn.Close(websocket.StatusOK, "")
			log.Printf("member %q disconnected", needsCloseName[i])
		}
	}
}

// Add adds a new member to the chat.
func (chat *Chat) Add(conn *websocket.Conn) {
	msg, err := conn.ReceiveText(64)
	parts := strings.Fields(msg)
	if err != nil || len(parts) < 2 || parts[0] != "CHAT" {
		log.Println("new member: connect failed,", err)
		conn.Close(websocket.StatusProtocolError, "")
		return
	}
	name := strings.Join(parts[1:], " ")

	chat.Lock()
	alreadyPresent := false
	for _, existing := range chat.members.names {
		if name == existing {
			alreadyPresent = true
			break
		}
	}
	if !alreadyPresent {
		chat.members.names = append(chat.members.names, name)
		chat.members.conns = append(chat.members.conns, conn)
		chat.change.Broadcast()
	}
	chat.Unlock()

	if alreadyPresent {
		log.Printf("name %q already in use, new member not connected", name)
		conn.Close(websocket.StatusInvalidData, "name already in use")
		return
	}
	log.Printf("member %q connected", name)
	chat.send <- &Message{
		When: time.Now(),
		Text: fmt.Sprintf("%q has joined this chat", name),
	}
}

// Remove a member from the chat.
func (chat *Chat) Remove(conn *websocket.Conn) {
	chat.Lock()
	idx := -1
	for i, memberConn := range chat.members.conns {
		if memberConn == conn {
			idx = i
			break
		}
	}
	if idx >= 0 {
		n := len(chat.members.conns) - 1
		if idx < n {
			chat.members.conns[idx] = chat.members.conns[n]
			chat.members.names[idx] = chat.members.names[n]
		}
		chat.members.conns = chat.members.conns[:n]
		chat.members.names = chat.members.names[:n]
	}
	chat.Unlock()
}

type members struct {
	names []string
	conns []*websocket.Conn
}

func (m *members) Copy() *members {
	res := &members{
		names: make([]string, len(m.names)),
		conns: make([]*websocket.Conn, len(m.conns)),
	}
	copy(res.names, m.names)
	copy(res.conns, m.conns)
	return res
}

// Message represents a text which is distributed to all chat members.
type Message struct {
	When time.Time
	From string
	Text string
}

func (msg *Message) asJSON() (string, error) {
	msgJSONBytes, err := json.Marshal(msg)
	if err != nil {
		return "", err
	}
	return string(msgJSONBytes), nil
}
