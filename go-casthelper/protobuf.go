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
	b = append(b, 0x08, 0x00)            // field 1 protocol_version = 0 (varint)
	b = appendField(b, 2, m.sourceID)    // field 2 source_id
	b = appendField(b, 3, m.destID)      // field 3 destination_id
	b = appendField(b, 4, m.namespace)   // field 4 namespace
	b = append(b, 0x28, 0x00)            // field 5 payload_type = 0 STRING (varint)
	b = appendField(b, 6, m.payloadUTF8) // field 6 payload_utf8
	logTrace("encode: src=%s dest=%s ns=%s payloadLen=%d -> %d wire bytes",
		m.sourceID, m.destID, shortNS(m.namespace), len(m.payloadUTF8), len(b))
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
			logWarn("decode: malformed tag varint at offset %d of %d (n=%d); stopping. bytes: %s",
				i, len(b), n, hexdump(b, 128))
			break
		}
		i += n
		field := int(tag >> 3)
		wire := tag & 7
		switch wire {
		case 0: // varint
			v, n := binary.Uvarint(b[i:])
			if n <= 0 {
				logWarn("decode: malformed varint for field %d at offset %d; stopping", field, i)
				return m
			}
			logTrace("decode: field %d varint = %d", field, v)
			i += n
		case 5: // 32-bit
			logTrace("decode: field %d fixed32, skipping", field)
			i += 4
		case 1: // 64-bit
			logTrace("decode: field %d fixed64, skipping", field)
			i += 8
		case 2: // length-delimited
			l, n := binary.Uvarint(b[i:])
			if n <= 0 {
				logWarn("decode: malformed length varint for field %d at offset %d; stopping", field, i)
				return m
			}
			i += n
			if i+int(l) > len(b) {
				logError("decode: field %d claims %d bytes but only %d remain — truncated frame; "+
					"decoded so far: src=%q dest=%q ns=%q payloadLen=%d",
					field, l, len(b)-i, m.sourceID, m.destID, m.namespace, len(m.payloadUTF8))
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
			case 7:
				logWarn("decode: message carries a binary payload (%d bytes) instead of payload_utf8 — not handled", l)
			default:
				logTrace("decode: ignoring unknown length-delimited field %d (%d bytes)", field, l)
			}
		default:
			logWarn("decode: unsupported wire type %d for field %d at offset %d; stopping", wire, field, i)
			return m
		}
	}
	return m
}
