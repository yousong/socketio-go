// Copyright (c) 2024 Yousong Zhou
// SPDX-License-Identifier: MIT

package socketio

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/pkg/errors"
)

const (
	PacketTypeCONNECT       = '0' // Used during the connection to a namespace.
	PacketTypeDISCONNECT    = '1' // Used when disconnecting from a namespace.
	PacketTypeEVENT         = '2' // Used to send data to the other side.
	PacketTypeACK           = '3' // Used to acknowledge an event.
	PacketTypeCONNECT_ERROR = '4' // Used during the connection to a namespace.
	PacketTypeBINARY_EVENT  = '5' // Used to send binary data to the other side.
	PacketTypeBINARY_ACK    = '6' // Used to acknowledge an event (the response includes binary data).

	ptMin = '0'
	ptMax = '6'
)

// Message describes socketio message on wire (excluding engine.io part)
type Message struct {
	Type      byte
	Namespace string
	AckId     int64
	Data      interface{}
	DataRaw   []byte
	// No support for attachments for now
}

// MarshalBinary implements encoding.BinaryMarshaler
//
// DataRaw field will be used as payload when it's nil.  Otherwise if Data
// field is not nil, the json marshaled version will be used as payload
func (msg *Message) MarshalBinary() ([]byte, error) {
	buf := &bytes.Buffer{}

	buf.WriteByte(msg.Type)
	if msg.Namespace != "/" {
		buf.WriteString(msg.Namespace)
		buf.WriteByte(',')
	}
	if msg.AckId != 0 {
		buf.WriteString(fmt.Sprintf("%d", msg.AckId))
	}
	if msg.DataRaw != nil {
		buf.Write(msg.DataRaw)
	} else if msg.Data != nil {
		enc := json.NewEncoder(buf)
		err := enc.Encode(msg.Data)
		if err != nil {
			return nil, errors.Wrap(err, "json encode payload")
		}
	}
	return buf.Bytes(), nil
}

// UnmarshalBinary implements encoding.BinaryUnmarshaler
//
// DataRaw field will be filled with payload bytes verbatim.
//
// Data field when not nil will be filled as target doing json unmarshal
func (msg *Message) UnmarshalBinary(data []byte) error {
	if len(data) < 1 {
		return errors.New("message is empty")
	}
	msg.Type = data[0]
	if msg.Type < ptMin && msg.Type > ptMin {
		return errors.New("malformed socketio message: invalid packet type")
	}
	msg.Namespace = "/"

	const (
		sepAttachment = '-'
		sepNamespace  = ','
	)
	const (
		orderInit = iota
		orderAttachment
		orderNamespace
		orderAckId
		orderPayload
	)

	//	```
	//	<packet type>[<# of binary attachments>-][<namespace>,][<acknowledgment id>][JSON-stringified payload without binary]
	//
	//	+ binary attachments extracted
	//	```
	//
	//	A Socket.IO packet contains the following fields:
	//
	//	- a packet type (integer)
	//	- a namespace (string)
	//	- optionally, a payload (Object | Array)
	//	- optionally, an acknowledgment id (integer)
	//
	// For a packet with ack id
	//
	//	The payload is mandatory and MUST be an array (possibly empty).
	//
	// For event packet
	//
	// 	The payload is mandatory and MUST be a non-empty array. If
	// 	that's not the case, then the receiver MUST close the
	// 	connection.
	//
	order := orderInit
	i := 1
	for j := i; j < len(data); j++ {
		c := data[j]
		if c >= '0' && c <= '9' {
			continue
		}
		switch c {
		case sepAttachment:
			return errors.New("unsupported feature: message with attachment")
		case sepNamespace:
			if order >= orderNamespace {
				return errors.New("malformed socketio message: namespace")
			}
			order = orderNamespace
			msg.Namespace = string(data[i:j])
			i = j + 1
		case '[', '\x7b' /* curly */, '"':
			ackIdData := data[i:j]
			if len(ackIdData) > 0 {
				if c != '[' {
					return errors.New("malformed socketio message: with ack id but payload is not an array")
				}
				if order >= orderAckId {
					return errors.New("malformed socketio message: ack id")
				}
				ackId, err := strconv.ParseInt(string(ackIdData), 10, 64)
				if err != nil {
					return errors.Wrap(err, "parse ack id")
				}
				msg.AckId = ackId
				order = orderAckId
			}
			if err := msg.unmarshalPayload(data[j:]); err != nil {
				return err
			}
			return nil
		}
	}
	if i < len(data) {
		// It's payload other than object, array, string
		// - true
		// - false
		// - null
		// - number
		//
		// The number is not for ack id: message with ack id is
		// required to have array payload
		if err := msg.unmarshalPayload(data[i:]); err != nil {
			return err
		}
	}
	return nil
}

func (msg *Message) unmarshalPayload(data []byte) error {
	if msg.Type == PacketTypeEVENT ||
		msg.Type == PacketTypeACK ||
		msg.Type == PacketTypeBINARY_EVENT ||
		msg.Type == PacketTypeBINARY_ACK {
		// weak validation for now
		if len(data) == 0 || data[0] != '[' {
			return errors.New("malformed socketio message: payload is required and must be an empty")
		}
	}
	msg.DataRaw = data
	if msg.Data != nil {
		b := bytes.NewBuffer(data)
		dec := json.NewDecoder(b)
		if err := dec.Decode(msg.Data); err != nil {
			return errors.Wrap(err, "json decode payload")
		}
	}
	return nil
}
