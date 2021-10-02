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
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"seehuhn.de/go/websocket"
)

type Chat struct {
	send chan<- *Message

	sync.Mutex
	clients map[string]*Client
}

type Client struct {
	name string
	conn *websocket.Conn
}

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

func NewChat() *Chat {
	c := make(chan *Message, 1)
	chat := &Chat{
		send:    c,
		clients: make(map[string]*Client),
	}
	go chat.handler(c)
	return chat
}

func (chat *Chat) handler(messages <-chan *Message) {
	for msg := range messages {
		msgJSON, err := msg.asJSON()
		if err != nil {
			log.Println("JSON encoding failed:", err)
			continue
		}

		chat.Lock()
		var closed []*Client
		for name, client := range chat.clients {
			err = client.conn.SendText(msgJSON)
			if err == websocket.ErrConnClosed {
				closed = append(closed, client)
			} else if err != nil {
				log.Printf("sending failed for %q: %s\n", name, err)
			}
		}
		for _, client := range closed {
			delete(chat.clients, client.name)
		}
		chat.Unlock()

		for _, client := range closed {
			client.conn.Close(websocket.StatusOK, "")
			log.Printf("client %q disconnected", client.name)
		}
	}
}

func (chat *Chat) Add(conn *websocket.Conn) {
	msg, err := conn.ReceiveText(64)
	parts := strings.Fields(msg)
	if err != nil || len(parts) < 2 || parts[0] != "CHAT" {
		log.Println("new client: connect failed,", err)
		conn.Close(websocket.StatusProtocolError, "")
		return
	}
	name := strings.Join(parts[1:], " ")

	client := &Client{
		name: name,
		conn: conn,
	}
	chat.Lock()
	_, alreadyPresent := chat.clients[name]
	if !alreadyPresent {
		chat.clients[name] = client
	}
	chat.Unlock()
	if alreadyPresent {
		log.Printf("name %q already in use, new client not connected", name)
		conn.Close(websocket.StatusInvalidData, "name already in use")
		return
	}
	log.Printf("client %q connected", name)
	chat.send <- &Message{
		When: time.Now(),
		Text: fmt.Sprintf("%q has joined this chat", name),
	}

	go func() {
		log.Printf("reader thread for %q started", name)
		for {
			msgText, err := conn.ReceiveText(1024)
			if err != nil {
				break
			}
			if msgText == "/quit" {
				err := conn.Close(websocket.StatusOK, "quit request received")
				if err != nil {
					log.Println("close error:", err)
				}
			} else if msgText == "/names" {
				var names []string
				chat.Lock()
				for name := range chat.clients {
					names = append(names, name)
				}
				chat.Unlock()

				chat.send <- &Message{
					When: time.Now(),
					Text: "members: " + strings.Join(names, ", "),
				}
			} else {
				chat.send <- &Message{
					When: time.Now(),
					From: name,
					Text: msgText,
				}
			}
		}
		chat.send <- &Message{
			When: time.Now(),
			Text: fmt.Sprintf("%q has left this chat", name),
		}
		log.Printf("reader thread for %q stopped", name)
	}()
}
