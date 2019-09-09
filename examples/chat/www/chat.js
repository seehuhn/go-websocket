var chat = chat || {};

chat.FIRST_NAMES = [
  'arrogant',
  'confused',
  'cute',
  'dancing',
  'fierce',
  'fluffy',
  'grey',
  'hypothetical',
  'lingering',
  'loitering',
  'naive',
  'pink',
  'singing',
  'snoozing',
  'strange'
];

chat.LAST_NAMES = [
  'axiom',
  'boy',
  'bunny rabit',
  'contradiction',
  'crocodile',
  'dream',
  'eel',
  'girl',
  'hippopotamus',
  'owl',
  'oxymoron',
  'philosopher',
  'rock',
  'sloth'
];

chat.connect = function() {
  var proto = (location.protocol == 'https:' ? 'wss:' : 'ws:')
  chat.URL = proto + '//' + location.host + '/api/chat';
  chat.ws = new WebSocket(chat.URL);

  var first = chat.FIRST_NAMES[Math.floor(Math.random() * chat.FIRST_NAMES.length)];
  var last = chat.LAST_NAMES[Math.floor(Math.random() * chat.LAST_NAMES.length)];
  var name = first + ' ' + last;

  chat.ws.addEventListener('open', function(event) {
    chat.ws.send('CHAT ' + name);
  });

  var messagesDiv = document.getElementById('messages');
  chat.ws.addEventListener('message', function(event) {
    var msg = JSON.parse(event.data);

    var p = document.createElement('P');
    if (msg.From == '') {
      p.className = 'server';
    } else if (msg.From == name) {
      p.className = 'myself';
    }

    var s1 = document.createElement('SPAN');
    d = new Date(msg.When);
    s1.innerText = d.toLocaleTimeString();
    s1.className = 'time';
    p.appendChild(s1);

    if (msg.From != '') {
      var s2 = document.createElement('SPAN');
      s2.innerText = msg.From;
      s2.className = 'sender';
      p.appendChild(s2);
    }

    var s3 = document.createElement('SPAN');
    s3.innerText = msg.Text;
    s3.className = 'text';
    p.appendChild(s3);

    messagesDiv.appendChild(p);
  });

  var sendBox = document.getElementById('sendbox');
  // Execute a function when the user releases a key on the keyboard
  sendBox.addEventListener('keydown', function(event) {
    if (event.keyCode === 13) {
      event.preventDefault();
      var text = sendBox.value;
      if (text == '') {
        return;
      }
      chat.ws.send(text);
      sendBox.value = '';
    }
  });

  var inputDiv = document.getElementById('input');
  chat.ws.addEventListener('close', function(event) {
    inputDiv.style.display = 'none';

    messagesDiv.appendChild(document.createElement('HR'));
  });
};
