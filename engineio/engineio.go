// Copyright (c) 2024 Yousong Zhou
// SPDX-License-Identifier: MIT
package engineio

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/pkg/errors"

	"github.com/yousong/socketio-go/internal"
)

var (
	ErrMalformedMessage = errors.New("malformed message")
)

var headerOrigin = &url.URL{
	Scheme: "https",
	Host:   "github.com",
	Path:   "yousong/engineio",
}

const (
	RequestPathEngineIO = "/engine.io/"
	EIO3                = "3"
	EIO4                = "4"
	TransportWebsocket  = "websocket"
)

const (
	packetTypeOpen    = '0' // Used during the handshake
	packetTypeClose   = '1' // Used to indicate that a transport can be closed.
	packetTypePing    = '2' // Used in the heartbeat mechanism
	packetTypePong    = '3' // Used in the heartbeat mechanism
	packetTypeMessage = '4' // Used to send a payload to the other side.
	packetTypeUpgrade = '5' // Used during the upgrade process
	packetTypeNoop    = '6' // Used during the upgrade process
)

// packetOpen is for the JSON struct in Engine.IO handshake
type packetOpen struct {
	Sid          string   `json:"sid"`          // The session ID.
	Upgrades     []string `json:"upgrades"`     // The list of available [transport upgrades](#upgrade).
	PingInterval uint64   `json:"pingInterval"` // The ping interval, used in the [heartbeat mechanism](#heartbeat) (in milliseconds).
	PingTimeout  uint64   `json:"pingTimeout"`  // The ping timeout, used in the [heartbeat mechanism](#heartbeat) (in milliseconds).
	MaxPayload   uint64   `json:"maxPayload"`   // The maximum number of bytes per chunk, used by the client to aggregate packets into [payloads](#packet-encoding).
}

// Config is for making an Engine.IO connection
type Config struct {
	// URL is the original URL.
	//
	// The scheme is expected to be either http or https.
	//
	// The package is responsible for deriving from it urls suitable for
	// Engine.IO protocol use, e.g. replacing the request path, adding EIO,
	// transport query arguments, etc.
	URL string
	// RequestPath is for HTTP request, it defaults to "/engine.io/"
	RequestPath string

	// EIO version to use
	EIOVersion string

	// OnError will be called with error occured when managing the
	// Engine.IO connection.
	//
	// Optional, errors will be ignored when the field is nil
	OnError func(error)
}

// Handler is func type for handling Engine.IO message payload (excluding packet type byte)
//
// Multiple handlers can be installed for the same packet type.  Data argument
// is expected to read-only to handlers
type Handler func(data []byte)

// Conn describes an Engine.IO connection
//
// Conn has background go routines for handling heartbeat etc.  Call Close()
// when done with the connection to release these resources
//
// Method calls return with error occurred in previous calls or in the
// background processing
type Conn struct {
	u           *url.URL
	open        *packetOpen
	heartbeatCh chan byte
	closeWg     *sync.WaitGroup
	closeCh     chan struct{}
	onError     func(error)

	// mu ensures only one concurrent writer on wsconn.
	mu       *sync.Mutex
	wsconn   *websocket.Conn
	errs     []error
	handlers map[byte][]Handler
}

// Close closes the Engine.IO connection, releasing associated resources
func (conn *Conn) Close() error {
	close(conn.closeCh)
	conn.closeWg.Wait()
	return conn.wsconn.Close()
}

// writePacket writes an Engine.IO packet.
//
// Concurrent calls will be serialized.
func (conn *Conn) writePacket(pktType byte, data []byte) error {
	conn.mu.Lock()
	defer conn.mu.Unlock()

	if err := conn.err_(); err != nil {
		return err
	}

	internal.Tracef("engineio >>> %c%s", pktType, data)
	// NOTE it seems some servers does not support websocket.BinaryMessage
	w, err := conn.wsconn.NextWriter(websocket.TextMessage)
	if err != nil {
		err = errors.Wrap(err, "ws next writer")
		conn.addErr_(err)
		return err
	}
	if _, err = w.Write([]byte{byte(pktType)}); err != nil {
		err = errors.Wrap(err, "ws write engine.io packet type")
		conn.addErr_(err)
		return err
	}
	if _, err = w.Write(data); err != nil {
		err = errors.Wrap(err, "ws write engine.io data")
		conn.addErr_(err)
		return err
	}
	if err := w.Close(); err != nil {
		err = errors.Wrap(err, "ws close writer")
		conn.addErr_(err)
		return err
	}
	return nil
}

// WriteMessage writes an Engine.IO message packet
//
// Concurrent calls will be serialized.
func (conn *Conn) WriteMessage(data []byte) error {
	return conn.writePacket(packetTypeMessage, data)
}

// addErr records the err.
//
// Future method calls will check recorded errors and return if it's not empty
func (conn *Conn) addErr(err error) {
	conn.mu.Lock()
	defer conn.mu.Unlock()
	conn.addErr_(err)
}

// addErr_ is like addErr but expects the caller to have already hold the lock
func (conn *Conn) addErr_(err error) {
	conn.errs = append(conn.errs, err)
	go conn.onError(err)
}

// Err returns recorded error
func (conn *Conn) Err() error {
	conn.mu.Lock()
	defer conn.mu.Unlock()
	return conn.err_()
}

// err_ is like Err in ways as addErr_ to addErr
func (conn *Conn) err_() error {
	if len(conn.errs) > 0 {
		// TODO aggregate errors
		return conn.errs[0]
	}
	return nil
}

// heartbeatLoop manages heartbeat turns.  It initiates ping and waits for
// pong.  It also handles received ping packets and send back pongs
func (conn *Conn) heartbeatLoop() {
	defer conn.closeWg.Done()

	open := conn.open
	interval := time.Duration(open.PingInterval) * time.Millisecond
	timeout := time.Duration(open.PingTimeout) * time.Millisecond
	pongCh := make(chan struct{})
	wg := &sync.WaitGroup{}
	defer wg.Wait()

	// recv
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case pktType := <-conn.heartbeatCh:
				switch pktType {
				case packetTypePing:
					err := conn.writePacket(packetTypePong, nil)
					if err != nil {
						internal.Tracef("engineio heartbeat: send pong: %v", err)
						return
					}
				case packetTypePong:
					pongCh <- struct{}{}
				}
			case <-conn.closeCh:
				return
			}
		}
	}()
	wg.Add(1)
	// initiate
	go func() {
		defer wg.Done()
		for {
			pingT := time.NewTimer(interval)
			select {
			case <-pingT.C:
				err := conn.writePacket(packetTypePing, nil)
				if err != nil {
					internal.Tracef("engineio heartbeat: send ping: %v", err)
					return
				}
			case <-pongCh:
				// skip stray pong packet
			case <-conn.closeCh:
				return
			}

			timeoutT := time.NewTimer(timeout)
			select {
			case <-timeoutT.C:
				conn.addErr(fmt.Errorf("heartbeat: timeout recv pong"))
				return
			case <-pongCh:
			case <-conn.closeCh:
				return
			}
		}
	}()
}

// recvLoop reads Engine.IO packets and delivers them according to packet type
func (conn *Conn) recvLoop() {
	defer conn.closeWg.Done()

	for {
		_, data, err := conn.wsconn.ReadMessage()
		if err != nil {
			conn.addErr(err)
			break
		}
		if len(data) < 1 {
			conn.addErr(errors.Wrap(ErrMalformedMessage, "too short"))
			return
		}
		internal.Tracef("engineio <<< %s", data)
		pktType := data[0]
		switch pktType {
		case packetTypePing, packetTypePong:
			tmr := time.NewTimer(time.Second)
			select {
			case conn.heartbeatCh <- pktType:
			case <-tmr.C:
				internal.Tracef("engineio handling received heartbeat packet: %c", pktType)
			}
		default:
			// TODO avoid message getting ignored before handlers
			// were installed
			handlers := conn.handlers[pktType]
			for _, handler := range handlers {
				handler(data[1:])
			}
		}
	}
}

func (conn *Conn) on(typ byte, handler Handler) {
	conn.mu.Lock()
	defer conn.mu.Unlock()
	conn.handlers[typ] = append(conn.handlers[typ], handler)
}

// OnMessage installs a handler for Engine.IO message packet.
//
// Multiple handlers can be installed for the same packet type
func (conn *Conn) OnMessage(handler Handler) {
	conn.on(packetTypeMessage, handler)
}

// DialContext tries to establish a engine.io session with config.
func DialContext(ctx context.Context, config Config) (*Conn, error) {
	// prepare websocket url
	urlstr := config.URL
	u, err := url.ParseRequestURI(urlstr)
	if err != nil {
		return nil, err
	}
	// NOTE only websocket is supported
	wsu := *u
	switch u.Scheme {
	case "http":
		wsu.Scheme = "ws"
	case "https":
		wsu.Scheme = "wss"
	default:
		return nil, fmt.Errorf("unknown url scheme: %s", u.Scheme)
	}
	// request path defaults to "/engine.io/"
	wsu.Path = config.RequestPath
	if wsu.Path == "" {
		wsu.Path = RequestPathEngineIO
	}
	// EIO defaults to "4"
	eioVer := config.EIOVersion
	if eioVer == "" {
		eioVer = EIO4
	}
	query := wsu.Query()
	query.Set("EIO", eioVer)
	query.Set("transport", TransportWebsocket)
	wsu.RawQuery = query.Encode()

	// make websocket connection
	internal.Tracef("engineio dial: %s", wsu.String())
	wsd := &websocket.Dialer{}
	wsconn, _, err := wsd.DialContext(ctx, wsu.String(), nil)
	if err != nil {
		return nil, err
	}

	// do handshake
	pktOpen, err := doHandshake(wsconn)
	if err != nil {
		defer wsconn.Close()
		return nil, errors.Wrap(err, "do handshake")
	}

	onError := config.OnError
	if onError == nil {
		onError = func(error) {}
	}
	conn := &Conn{
		u:           u,
		open:        pktOpen,
		heartbeatCh: make(chan byte),
		closeWg:     &sync.WaitGroup{},
		closeCh:     make(chan struct{}),
		onError:     onError,

		mu:     &sync.Mutex{},
		wsconn: wsconn,

		handlers: map[byte][]Handler{},
	}
	conn.closeWg.Add(2)
	go conn.heartbeatLoop()
	go conn.recvLoop()

	return conn, nil
}

func doHandshake(wsconn *websocket.Conn) (*packetOpen, error) {
	msgType, d, err := wsconn.ReadMessage()
	if err != nil {
		return nil, err
	}
	if msgType != websocket.TextMessage {
		return nil, fmt.Errorf("unexpected message type %s for handshake", msgType)
	}
	if len(d) < 3 {
		// Expect JSON dict response: "0{}"
		return nil, fmt.Errorf("message too short (%d) for a handshake packet", len(d))
	}
	if typ := d[0]; typ != packetTypeOpen {
		return nil, fmt.Errorf("expecting open packet (%d), got %d", packetTypeOpen, typ)
	}
	internal.Tracef("engineio handshake: %s", d[1:])
	pktOpen := &packetOpen{}
	if err := json.Unmarshal(d[1:], pktOpen); err != nil {
		return nil, fmt.Errorf("unmarshal open packet response: %v", err)
	}
	return pktOpen, nil
}
