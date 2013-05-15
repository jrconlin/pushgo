package simplepush

import (
    "code.google.com/p/go.net/websocket"
    "mozilla.org/simplepush/storage"

    "log"
)

const (
    UNREG = iota
    REGIS
    HELLO
    ACK
    FLUSH
    RETRN
)

type PushCommand struct {
    // Use mutable int value
    Command int            //command type (UNREG, REGIS, ACK, etc)
    Arguments interface {} //command arguments
}

type PushWS struct {
    Uaid string             // id
    Socket *websocket.Conn  // Remote connection
    Done chan bool          // thread close flag
    Scmd chan PushCommand   // server command channel
    Ccmd chan PushCommand   // client command channel
    Store *storage.Storage
}

func (sock PushWS) Close() error {
    log.Printf("INFO : Closing socket %s \n", sock.Uaid)
    sock.Scmd <- PushCommand{UNREG, sock.Uaid}
    sock.Done <- true
    // remove from the map registry
    return nil
}


