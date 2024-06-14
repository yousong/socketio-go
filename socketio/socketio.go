// Copyright (c) 2024 Yousong Zhou
// SPDX-License-Identifier: MIT
package socketio

import (
	"context"
	"fmt"
	"net/url"
	"sync"

	"github.com/pkg/errors"

	"github.com/yousong/socketio-go/engineio"
	"github.com/yousong/socketio-go/internal"
)

const (
	// RequestPathSocketIO is the default request path when making socketio
	// connection
	RequestPathSocketIO = "/socket.io/"
)

// Config is for makeing Socket.IO connection
type Config struct {
	// URL is the original URL.
	//
	// The scheme is expected to be either http or https.
	//
	// The package is responsible for deriving from it urls suitable for
	// Socket.IO protocol use, e.g. replacing the request path, adding EIO,
	// transport query arguments, etc.
	URL string
	// EIO version to use
	EIOVersion string

	// OnError will be called with error occured when managing the
	// Socket.IO connection.
	//
	// Optional, errors will be ignored when the field is nil
	OnError func(error)
}

// Conn describes a Socket.IO connection
type Conn struct {
	mu         *sync.Mutex
	eioconn    *engineio.Conn
	errs       []error
	namespaces map[string]*Namespace
	onError    func(error)
}

// DialContext tries to establish a Socket.IO session with config.
func DialContext(ctx context.Context, config Config) (*Conn, error) {
	if config.OnError == nil {
		config.OnError = func(error) {}
	}
	eioconn, err := engineio.DialContext(ctx, engineio.Config{
		URL:         config.URL,
		RequestPath: RequestPathSocketIO,
		EIOVersion:  config.EIOVersion,
		OnError:     config.OnError,
	})
	if err != nil {
		return nil, err
	}
	conn := &Conn{
		mu:         &sync.Mutex{},
		eioconn:    eioconn,
		namespaces: map[string]*Namespace{},
		onError:    config.OnError,
	}
	eioconn.OnMessage(conn.onEioMessage)
	return conn, nil
}

// Close closes the Socket.IO connection
func (conn *Conn) Close() error {
	return conn.eioconn.Close()
}

func (conn *Conn) addErr(err error) {
	conn.mu.Lock()
	defer conn.mu.Unlock()
	conn.addErr_(err)
}
func (conn *Conn) addErr_(err error) {
	conn.errs = append(conn.errs, err)
	go conn.onError(err)
}
func (conn *Conn) addErrEio_(err error) {
	conn.errs = append(conn.errs, err)
}
func (conn *Conn) Err() error {
	conn.mu.Lock()
	defer conn.mu.Unlock()
	return conn.err_()
}
func (conn *Conn) err_() error {
	if len(conn.errs) > 0 {
		return conn.errs[0]
	}
	return nil
}

func (conn *Conn) onEioMessage(data []byte) {
	conn.mu.Lock()
	defer conn.mu.Unlock()

	internal.Tracef("socketio <<< %s", data)
	msg := Message{}
	pmsg := &msg
	if err := pmsg.UnmarshalBinary(data); err != nil {
		internal.Tracef("socketio unmarshal error: %v", err)
		conn.addErr_(err)
		return
	}
	ns := conn.namespaces[msg.Namespace]
	if ns == nil {
		return
	}
	go ns.onMessage(msg)
}

func (conn *Conn) nsAdd(ns *Namespace) error {
	conn.mu.Lock()
	defer conn.mu.Unlock()

	name := ns.Name
	u, err := url.Parse(name)
	if err != nil {
		return errors.Wrap(err, "parse namespace")
	}

	nameNormalized := u.Path
	if _, ok := conn.namespaces[nameNormalized]; ok {
		return errors.Wrap(err, "namespace already connected")
	}

	conn.namespaces[nameNormalized] = ns
	return nil
}

func (conn *Conn) nsDel(nameNormalized string) {
	conn.mu.Lock()
	defer conn.mu.Unlock()

	delete(conn.namespaces, nameNormalized)
}

func (conn *Conn) Connect(ctx context.Context, ns *Namespace) error {
	if ns.PacketHandlers == nil {
		ns.PacketHandlers = map[byte]Handler{}
	}
	errCh := make(chan error)
	ns.PacketHandlers[PacketTypeCONNECT] = func(msg Message) {
		select {
		case errCh <- nil:
		default:
		}
	}
	ns.PacketHandlers[PacketTypeCONNECT_ERROR] = func(msg Message) {
		conn.nsDel(msg.Namespace)
		select {
		case errCh <- fmt.Errorf("%s", msg.DataRaw):
		default:
		}
	}
	ns.PacketHandlers[PacketTypeDISCONNECT] = func(msg Message) {
		conn.nsDel(msg.Namespace)
	}

	// add ns to registry
	if err := conn.nsAdd(ns); err != nil {
		return err
	}

	msg := Message{
		Type:      PacketTypeCONNECT,
		Namespace: ns.Name,
	}
	internal.Tracef("socketio connecting to namespace %s", msg.Namespace)
	if err := conn.writeMessage(msg); err != nil {
		return errors.Wrap(err, "send socketio connect")
	}
	select {
	case <-ctx.Done():
		// remove ns from registry
		conn.nsDel(ns.nameNormalized)
		return ctx.Err()
	case err := <-errCh:
		if err != nil {
			return errors.Wrap(err, "recv socketio connect resp")
		}
		return nil
	}
}

func (conn *Conn) writeMessage(msg Message) error {
	data, err := msg.MarshalBinary()
	if err != nil {
		return errors.Wrap(err, "marshal socketio message")
	}

	conn.mu.Lock()
	defer conn.mu.Unlock()

	if err := conn.err_(); err != nil {
		return err
	}
	if err := conn.eioconn.WriteMessage(data); err != nil {
		conn.addErrEio_(err)
		return err
	}
	return nil
}

type Handler func(msg Message)

type Namespace struct {
	Name           string
	nameNormalized string
	PacketHandlers map[byte]Handler
}

func (ns *Namespace) onMessage(msg Message) {
	h := ns.PacketHandlers[msg.Type]
	if h == nil {
		return
	}
	h(msg)
}
