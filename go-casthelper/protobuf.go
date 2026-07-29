package main

import "encoding/binary"

// castMessage is the subset of the Cast v2 `CastMessage` protobuf we need.
//
// The full proto (from the Cast SDK) is:
//
//	message CastMessage {
//	  required ProtocolVersion protocol_version = 1; // enum, we always send 0
//	  required string source_id      = 2;
//	  required string destination_id = 3;
//	  required string namespace      = 4;
//	  required PayloadType payload_type = 5;          // enum, 0 = STRING
//	  optional string payload_utf8   = 6;             // JSON lives here
//	  optional bytes  payload_binary = 7;             // unused by us
//	}
//
// Every payload we care about is JSON in payload_utf8, so we hand-encode the
// handful of fields instead of pulling in protoc + the protobuf runtime.
type castMessage struct {
	sourceID    string
	destID      string
	namespace   string
	payloadUTF8 string
}

// appendField writes a length-delimited (wire type 2) string field.
func appendField(b []byte, field int, s string) []byte {
	b = append(b, byte(field<<3|2)) // tag = field number << 3 | wire type
	b = binary.AppendUvarint(b, uint64(len(s)))
	return append(b, s...)
}

// encodeCastMessage serializes a castMessage to protobuf wire bytes.
func encodeCastMessage(m castMessage) []byte {
	var b []byte
	b = append(b, 0x08, 0x00)             // field 1 protocol_version = 0 (varint)
	b = appendField(b, 2, m.sourceID)     // field 2 source_id
	b = appendField(b, 3, m.destID)       // field 3 destination_id
	b = appendField(b, 4, m.namespace)    // field 4 namespace
	b = append(b, 0x28, 0x00)             // field 5 payload_type = 0 STRING (varint)
	b = appendField(b, 6, m.payloadUTF8)  // field 6 payload_utf8
	return b
}

// decodeCastMessage walks the wire format and pulls out the fields we read.
// It tolerates unknown fields and the (unused) binary payload.
func decodeCastMessage(b []byte) castMessage {
	var m castMessage
	i := 0
	for i < len(b) {
		tag, n := binary.Uvarint(b[i:])
		if n <= 0 {
			break
		}
		i += n
		field := int(tag >> 3)
		switch tag & 7 { // wire type
		case 0: // varint
			_, n := binary.Uvarint(b[i:])
			i += n
		case 5: // 32-bit
			i += 4
		case 1: // 64-bit
			i += 8
		case 2: // length-delimited
			l, n := binary.Uvarint(b[i:])
			i += n
			if i+int(l) > len(b) {
				return m
			}
			val := string(b[i : i+int(l)])
			i += int(l)
			switch field {
			case 2:
				m.sourceID = val
			case 3:
				m.destID = val
			case 4:
				m.namespace = val
			case 6:
				m.payloadUTF8 = val
			}
		default:
			return m
		}
	}
	return m
}
