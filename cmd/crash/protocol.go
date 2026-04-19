package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
)

type opKind string

const (
	// op* values identify logical workload operation kinds.
	opPut    opKind = "put"
	opDelete opKind = "delete"
	opBatch  opKind = "batch"

	eventReady eventKind = "ready"
	eventStart eventKind = "start"
	eventAck   eventKind = "ack"
	eventFail  eventKind = "fail"

	profileCI   = "ci"
	profileFull = "full"
)

// eventKind identifies worker lifecycle messages emitted to controller.
type eventKind string

// batchItem is one operation nested inside a batch workload operation.
type batchItem struct {
	Kind  opKind
	Key   []byte
	Value []byte
}

// operation is the in-memory representation of a workload step.
type operation struct {
	ID    int
	Kind  opKind
	Key   []byte
	Value []byte
	Batch []batchItem
}

// operationMessage is the wire format for operation NDJSON messages.
type operationMessage struct {
	Kind     opKind             `json:"kind"`
	OpID     int                `json:"op_id"`
	KeyB64   string             `json:"key_b64,omitempty"`
	ValueB64 string             `json:"value_b64,omitempty"`
	Batch    []batchItemMessage `json:"batch,omitempty"`
}

// batchItemMessage is the wire format for a batch sub-operation.
type batchItemMessage struct {
	Kind     opKind `json:"kind"`
	KeyB64   string `json:"key_b64,omitempty"`
	ValueB64 string `json:"value_b64,omitempty"`
}

// eventMessage is the wire format for worker lifecycle events.
type eventMessage struct {
	Kind  eventKind `json:"kind"`
	OpID  int       `json:"op_id,omitempty"`
	Error string    `json:"error,omitempty"`
	Point string    `json:"point,omitempty"`
}

// toMessage encodes an operation into a binary-safe wire message.
func (op operation) toMessage() operationMessage {
	msg := operationMessage{
		Kind: op.Kind,
		OpID: op.ID,
	}

	switch op.Kind {
	case opPut:
		msg.KeyB64 = encodeB64(op.Key)
		msg.ValueB64 = encodeB64(op.Value)
	case opDelete:
		msg.KeyB64 = encodeB64(op.Key)
	case opBatch:
		msg.Batch = make([]batchItemMessage, len(op.Batch))
		for i, item := range op.Batch {
			msg.Batch[i] = batchItemMessage{
				Kind:     item.Kind,
				KeyB64:   encodeB64(item.Key),
				ValueB64: encodeB64(item.Value),
			}
		}
	}

	return msg
}

// toOperation decodes a wire message back into an operation.
func (msg operationMessage) toOperation() (operation, error) {
	op := operation{
		ID:   msg.OpID,
		Kind: msg.Kind,
	}

	switch msg.Kind {
	case opPut:
		var err error
		op.Key, err = decodeB64(msg.KeyB64)
		if err != nil {
			return operation{}, fmt.Errorf("decoding put key: %w", err)
		}
		op.Value, err = decodeB64(msg.ValueB64)
		if err != nil {
			return operation{}, fmt.Errorf("decoding put value: %w", err)
		}
	case opDelete:
		var err error
		op.Key, err = decodeB64(msg.KeyB64)
		if err != nil {
			return operation{}, fmt.Errorf("decoding delete key: %w", err)
		}
	case opBatch:
		op.Batch = make([]batchItem, len(msg.Batch))
		for i, item := range msg.Batch {
			key, err := decodeB64(item.KeyB64)
			if err != nil {
				return operation{}, fmt.Errorf("decoding batch key: %w", err)
			}
			value, err := decodeB64(item.ValueB64)
			if err != nil {
				return operation{}, fmt.Errorf("decoding batch value: %w", err)
			}
			op.Batch[i] = batchItem{
				Kind:  item.Kind,
				Key:   key,
				Value: value,
			}
		}
	default:
		return operation{}, fmt.Errorf("unsupported operation kind %q", msg.Kind)
	}

	return op, nil
}

// encodeNDJSON marshals a value and appends one newline frame delimiter.
func encodeNDJSON(v any) ([]byte, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// encodeB64 encodes binary payloads for JSON transport.
func encodeB64(data []byte) string {
	if len(data) == 0 {
		return ""
	}
	return base64.StdEncoding.EncodeToString(data)
}

// decodeB64 decodes base64 values, treating empty string as nil.
func decodeB64(value string) ([]byte, error) {
	if value == "" {
		return nil, nil
	}
	data, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		return nil, err
	}
	return data, nil
}

// eventBytes serializes one event into newline-delimited JSON bytes.
func eventBytes(event eventMessage) []byte {
	data, err := encodeNDJSON(event)
	if err != nil {
		panic(err)
	}
	return data
}

// cloneBytes returns a nil-preserving copy.
func cloneBytes(src []byte) []byte {
	if src == nil {
		return nil
	}
	return bytes.Clone(src)
}
