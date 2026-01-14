package protocol

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
)

type MessageType uint8

const (
	MsgListSessions MessageType = iota + 1
	MsgSessionList
	MsgCreateSession
	MsgDeleteSession
	MsgAttach
	MsgDetach
	MsgResize
	MsgInput
	MsgOutput
	MsgError
	MsgOK
)

type Message struct {
	Type    MessageType     `json:"type"`
	Payload json.RawMessage `json:"payload,omitempty"`
}

type SessionInfo struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Created int64  `json:"created"`
	Clients int    `json:"clients"`
}

type SessionListPayload struct {
	Sessions []SessionInfo `json:"sessions"`
}

type CreateSessionPayload struct {
	Name string `json:"name"`
	Cols int    `json:"cols"`
	Rows int    `json:"rows"`
}

type AttachPayload struct {
	SessionID string `json:"session_id"`
	Cols      int    `json:"cols"`
	Rows      int    `json:"rows"`
}

type ResizePayload struct {
	Cols int `json:"cols"`
	Rows int `json:"rows"`
}

type DeleteSessionPayload struct {
	SessionID string `json:"session_id"`
}

type ErrorPayload struct {
	Message string `json:"message"`
}

type OutputPayload struct {
	Data []byte `json:"data"`
}

type InputPayload struct {
	Data []byte `json:"data"`
}

func WriteMessage(w io.Writer, msg *Message) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal message: %w", err)
	}

	length := uint32(len(data))
	if err := binary.Write(w, binary.BigEndian, length); err != nil {
		return fmt.Errorf("write length: %w", err)
	}

	if _, err := w.Write(data); err != nil {
		return fmt.Errorf("write data: %w", err)
	}

	return nil
}

func ReadMessage(r io.Reader) (*Message, error) {
	var length uint32
	if err := binary.Read(r, binary.BigEndian, &length); err != nil {
		return nil, fmt.Errorf("read length: %w", err)
	}

	if length > 1024*1024 {
		return nil, fmt.Errorf("message too large: %d", length)
	}

	data := make([]byte, length)
	if _, err := io.ReadFull(r, data); err != nil {
		return nil, fmt.Errorf("read data: %w", err)
	}

	var msg Message
	if err := json.Unmarshal(data, &msg); err != nil {
		return nil, fmt.Errorf("unmarshal message: %w", err)
	}

	return &msg, nil
}

func NewMessage(t MessageType, payload any) (*Message, error) {
	msg := &Message{Type: t}
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return nil, err
		}
		msg.Payload = data
	}
	return msg, nil
}

func ParsePayload[T any](msg *Message) (*T, error) {
	var payload T
	if err := json.Unmarshal(msg.Payload, &payload); err != nil {
		return nil, err
	}
	return &payload, nil
}
