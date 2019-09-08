Websocket
=========

Websocket is a http handler for Go, which initiates websocket_
connections.

.. _websocket: https://en.wikipedia.org/wiki/WebSocket

Copyright (C) 2019  Jochen Voss

This program is free software: you can redistribute it and/or modify
it under the terms of the GNU General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.

Installation
------------

This package can be installed using the ``go get`` command::

  go get seehuhn.de/go/websocket

Usage
-----

Documentation is available via the package's online help, either on
godoc.org_ or on the command line::

  go doc -all seehuhn.de/go/websocket

For demonstation, here is a simple echo server.  To install the handler
at a given URL, use a ``websocket.Handler`` object::

  func main() {
	  websocketHandler := &websocket.Handler{
		  Handle: echo,
	  }
	  http.Handle("/", websocketHandler)
	  log.Fatal(http.ListenAndServe(":8080", nil))
  }

The function ``echo()`` will be called every time a client makes a
connection::

  func echo(conn *websocket.Conn) {
	  defer conn.Close(websocket.StatusOK, "")

	  for {
		  tp, r, err := conn.ReceiveMessage()
		  if err != nil {
			  break
		  }

		  w, err := conn.SendMessage(tp)
		  if err != nil {
			  // We still need to read the complete message, so that the
			  // next read doesn't block.
			  io.Copy(ioutil.Discard, r)
			  break
		  }

		  _, err = io.Copy(w, r)
		  if err != nil {
			  io.Copy(ioutil.Discard, r)
		  }

		  w.Close()
	  }
  }

.. _godoc.org: https://godoc.org/seehuhn.de/go/websocket
