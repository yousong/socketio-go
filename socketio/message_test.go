// Copyright (c) 2024 Yousong Zhou
// SPDX-License-Identifier: MIT
package socketio

import (
	"reflect"
	"testing"
)

func TestMessagge(t *testing.T) {
	it := func(desc string, msg Message) {
		t.Run(desc, func(t *testing.T) {
			data, err := msg.MarshalBinary()
			if err != nil {
				t.Fatalf("marshal %v", err)
			}
			msg1 := Message{}
			pmsg1 := &msg1
			if err := pmsg1.UnmarshalBinary(data); err != nil {
				t.Fatalf("unmarshal %v", err)
			}
			if !reflect.DeepEqual(msg, msg1) {
				t.Fatalf("msg1 not equal to msg after marshal/unmarshal:\n"+
					"msg : %#v\n"+
					"msg1: %#v",
					msg, msg1)
			}
		})
	}
	// The following cases were taken from test/parser.js
	// (https://github.com/socketio/socket.io-parser)
	it("encodes connection", Message{
		Type:      PacketTypeCONNECT,
		Namespace: "/woot",
		DataRaw:   []byte(`{"token": "123"}`),
	})

	it("encodes disconnection", Message{
		Type:      PacketTypeDISCONNECT,
		Namespace: "/woot",
	})

	it("encodes an event", Message{
		Type:      PacketTypeEVENT,
		DataRaw:   []byte(`["a", 1, {}]`),
		Namespace: "/",
	})

	it("encodes an event (with an integer as event name)", Message{
		Type:      PacketTypeEVENT,
		DataRaw:   []byte(`[1, "a", {}]`),
		Namespace: "/",
	})

	it("encodes an event (with ack)", Message{
		Type:      PacketTypeEVENT,
		DataRaw:   []byte(`["a", 1, {}]`),
		AckId:     1,
		Namespace: "/test",
	})

	it("encodes an ack", Message{
		Type:      PacketTypeACK,
		DataRaw:   []byte(`["a", 1, {}]`),
		AckId:     123,
		Namespace: "/",
	})

	it("encodes an connect error", Message{
		Type:      PacketTypeCONNECT_ERROR,
		DataRaw:   []byte(`Unauthorized`),
		Namespace: "/",
	})

	it("encodes an connect error (with object)", Message{
		Type:      PacketTypeCONNECT_ERROR,
		DataRaw:   []byte(`{"message": "Unauthorized"}`),
		Namespace: "/",
	})
}
