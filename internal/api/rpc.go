package api

import (
	"encoding/json"
	"fmt"
)

// Version is the JSON-RPC protocol version every message carries.
const Version = "2.0"

// APIVersion is the version of the Footman method set (ADR-0018).
const APIVersion = "v1"

// MaxMessage is the largest single message (one line) accepted, in bytes.
const MaxMessage = 1 << 20

// Standard JSON-RPC 2.0 error codes.
const (
	CodeParseError     = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternal       = -32603
)

// Footman error codes, in the range JSON-RPC reserves for servers.
const (
	// CodeFailed is a method that ran and failed.
	CodeFailed = -32000
	// CodeBusy means the pipeline is handling another utterance.
	CodeBusy = -32001
	// CodeNotFound means the job, subscription or key does not exist.
	CodeNotFound = -32002
	// CodeUnavailable means the feature is not wired in this daemon (for
	// example jobs without a worker).
	CodeUnavailable = -32003
)

// Request is a JSON-RPC request, or a notification when ID is empty.
type Request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

// IsNotification reports whether r expects no response.
func (r *Request) IsNotification() bool { return len(r.ID) == 0 }

// Response is a JSON-RPC response. Exactly one of Result and Error is set.
type Response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

// Error is a JSON-RPC error object. It is also the Go error returned by
// Client.Call when the server answers with an error.
type Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func (e *Error) Error() string { return fmt.Sprintf("%s (code %d)", e.Message, e.Code) }

// Errorf returns an *Error with the given code.
func Errorf(code int, format string, args ...any) *Error {
	return &Error{Code: code, Message: fmt.Sprintf(format, args...)}
}

// Notification is a message without an ID: from the server, an event.
type Notification struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params,omitempty"`
}

// message is the union used to tell requests, responses and notifications
// apart when decoding.
type message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

// validID reports whether raw is a JSON-RPC id: a string, a number or null.
func validID(raw json.RawMessage) bool {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return false
	}
	switch v.(type) {
	case string, float64, nil:
		return true
	}
	return false
}
