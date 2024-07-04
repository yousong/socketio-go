// Copyright (c) 2024 Yousong Zhou
// SPDX-License-Identifier: MIT

package internal

import (
	"log"
	"os"
)

var debugLevel = 0

func init() {
	v := os.Getenv("SOCKETIO_DEBUG")
	if v == "trace" {
		debugLevel = 1
	}
}

func Tracef(f string, args ...interface{}) {
	if debugLevel > 0 {
		log.Printf("[TRACE] "+f, args...)
	}
}
