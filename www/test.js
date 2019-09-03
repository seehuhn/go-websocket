var test = test || {};

test.hello = function() {
  console.log("hello");

  test.ws = new WebSocket("ws://localhost:8080/test", ["p1", "p2", "p3"]);

  test.ws.addEventListener('open', function (event) {
    console.log(test.ws.protocol);
    test.ws.send('Hello Server!');
    test.ws.send('Noptaloo');
    // test.ws.close(3333, 'morkel');
  });

  test.ws.addEventListener('message', function (event) {
    console.log('Message from server ', event.data);
  });
};
