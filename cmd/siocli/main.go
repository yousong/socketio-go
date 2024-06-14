// Copyright (c) 2024 Yousong Zhou
// SPDX-License-Identifier: MIT
package main

import (
	"context"
	"flag"
	"fmt"
	"net/url"
	"os"
	"time"

	"github.com/golang/glog"

	"github.com/yousong/socketio-go/socketio"
)

func main() {
	var (
		useEIOVersion int
		timeoutSec    int
	)
	flag.IntVar(&useEIOVersion, "eio", 4, "engine.io version (EIO query argument)")
	flag.IntVar(&timeoutSec, "timeout", 5, "timeout in seconds for establishing a session")
	flag.Lookup("logtostderr").Value.Set("true")
	flag.Parse()
	args := flag.Args()
	if len(args) != 1 {
		flag.Usage()
		return
	}

	// parse url
	urlstr := args[0]
	u, err := url.Parse(urlstr)
	if err != nil {
		glog.Errorf("parse url: %v", err)
		os.Exit(1)
	}

	ctx := context.Background()
	ctx, cancel := context.WithTimeout(ctx, time.Duration(timeoutSec)*time.Second)
	defer cancel()

	// dial
	conn, err := socketio.DialContext(ctx, socketio.Config{
		URL:        urlstr,
		EIOVersion: fmt.Sprintf("%d", useEIOVersion),
	})
	if err != nil {
		glog.Errorf("dial socketio: %v", err)
		os.Exit(1)
	}
	defer conn.Close()

	// connect to namespace
	ns := &socketio.Namespace{
		Name: u.RequestURI(),
		PacketHandlers: map[byte]socketio.Handler{
			socketio.PacketTypeEVENT: func(msg socketio.Message) {
				glog.Infof("sockio event: %#v", msg)
			},
		},
	}
	if err := conn.Connect(ctx, ns); err != nil {
		glog.Errorf("connect: %v", err)
	}
	glog.Infof("connected")
	ch := make(chan int)
	<-ch
	// defer conn.Disconnect(ctx,ns)
}
