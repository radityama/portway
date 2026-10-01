package protocol

import (
	"encoding/json"
	"net/http"
	"net/textproto"
	"net/url"
	"strings"
	"unicode/utf8"
)

const (
	MaxDataSize               = 16 * 1024
	MaxOpenPayloadSize        = 64 * 1024
	MaxHTTPHeaderSize         = 32 * 1024
	MaxRequestBodySize  int64 = 16 * 1024 * 1024
	MaxResponseBodySize int64 = 64 * 1024 * 1024
)

type OpenStream struct {
	Method        string     `json:"method"`
	Target        string     `json:"target"`
	Host          string     `json:"host"`
	Headers       [][]string `json:"headers"`
	ContentLength int64      `json:"content_length"`
	Upgrade       string     `json:"upgrade,omitempty"`
}

func HTTPToken(s string) bool {
	if len(s) == 0 {
		return false
	}
	for i := range len(s) {
		c := s[i]
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("!#$%&'*+-.^_`|~", rune(c)) {
			continue
		}
		return false
	}
	return true
}
func ForbiddenHeader(name string) bool {
	switch strings.ToLower(name) {
	case "connection", "proxy-connection", "keep-alive", "proxy-authenticate", "proxy-authorization", "te", "trailer", "transfer-encoding", "upgrade", "host", "content-length":
		return true
	}
	return false
}
func (o OpenStream) Validate() error {
	if !HTTPToken(o.Method) || len(o.Method) > 32 || strings.EqualFold(o.Method, "CONNECT") || !ValidHostname(o.Host) || len(o.Target) == 0 || len(o.Target) > 8192 || o.Target[0] != '/' || o.ContentLength < -1 || o.ContentLength > MaxRequestBodySize || o.Headers == nil || len(o.Headers) > 128 {
		return ErrInvalidHandshake
	}
	u, err := url.ParseRequestURI(o.Target)
	if err != nil || u.IsAbs() || u.Host != "" || u.Fragment != "" || !utf8.ValidString(o.Target) {
		return ErrInvalidHandshake
	}
	total := len(o.Target) + len(o.Host) + len(o.Method)
	for _, pair := range o.Headers {
		if len(pair) != 2 || !HTTPToken(pair[0]) || textproto.CanonicalMIMEHeaderKey(pair[0]) != pair[0] || ForbiddenHeader(pair[0]) || !utf8.ValidString(pair[1]) {
			return ErrInvalidHandshake
		}
		for i := range len(pair[1]) {
			c := pair[1][i]
			if c < 32 && c != '\t' || c == 127 {
				return ErrInvalidHandshake
			}
		}
		total += len(pair[0]) + len(pair[1]) + 4
	}
	if total > MaxHTTPHeaderSize {
		return ErrPayloadTooLarge
	}
	if o.Upgrade != "" {
		h := make(http.Header)
		for _, pair := range o.Headers {
			h.Add(pair[0], pair[1])
		}
		if o.Upgrade != "websocket" || o.Method != "GET" || o.ContentLength != 0 || !ValidWebSocketMetadata(h) {
			return ErrInvalidHandshake
		}
	}
	return nil
}

func EncodeOpenStream(id uint64, o OpenStream) (Frame, error) {
	if err := o.Validate(); err != nil {
		return Frame{}, err
	}
	payload, err := json.Marshal(o)
	if err != nil || len(payload) > MaxOpenPayloadSize {
		return Frame{}, ErrPayloadTooLarge
	}
	f := Frame{Version: Version, Type: TypeOpenStream, Payload: payload}
	f.StreamID = id
	if err := f.Validate(); err != nil {
		return Frame{}, err
	}
	return f, nil
}
func DecodeOpenStream(f Frame) (OpenStream, error) {
	var o OpenStream
	if err := f.Validate(); err != nil {
		return o, err
	}
	if f.Type != TypeOpenStream || len(f.Payload) > MaxOpenPayloadSize {
		return o, ErrInvalidHandshake
	}
	keys := []string{"method", "target", "host", "headers", "content_length"}
	allowed := append(append([]string{}, keys...), "upgrade")
	if err := decodeObject(f.Payload, &o, allowed, keys, allowed); err != nil {
		return OpenStream{}, err
	}
	// An explicitly present upgrade must select a supported protocol.
	var fields map[string]json.RawMessage
	_ = json.Unmarshal(f.Payload, &fields)
	if _, exists := fields["upgrade"]; exists && o.Upgrade == "" {
		return OpenStream{}, ErrInvalidHandshake
	}
	if err := o.Validate(); err != nil {
		return OpenStream{}, err
	}
	return o, nil
}

const (
	StreamUnavailable = "UPSTREAM_UNAVAILABLE"
	StreamLimit       = "STREAM_LIMIT"
	StreamTimeout     = "STREAM_TIMEOUT"
	StreamCancelled   = "STREAM_CANCELLED"
	StreamBodyLimit   = "BODY_LIMIT"
	StreamInvalid     = "STREAM_INVALID"
	StreamDraining    = "STREAM_DRAINING"
)

type StreamError struct {
	Code string `json:"code"`
}

func validStreamCode(code string) bool {
	return code == StreamUnavailable || code == StreamLimit || code == StreamTimeout || code == StreamCancelled || code == StreamBodyLimit || code == StreamInvalid || code == StreamDraining
}
func EncodeStreamControl(typ Type, id uint64, code string) (Frame, error) {
	var f Frame
	var err error
	switch typ {
	case TypeOpenStreamOK, TypeCloseStream:
		if code != "" {
			return f, ErrInvalidHandshake
		}
		f, err = encodeHandshake(typ, struct{}{})
	case TypeOpenStreamError, TypeResetStream:
		if !validStreamCode(code) {
			return f, ErrInvalidHandshake
		}
		f, err = encodeHandshake(typ, StreamError{Code: code})
	default:
		return f, ErrUnexpectedType
	}
	if err != nil {
		return f, err
	}
	f.StreamID = id
	if err := f.Validate(); err != nil {
		return Frame{}, err
	}
	return f, nil
}
func DecodeStreamControl(f Frame) (string, error) {
	if err := f.Validate(); err != nil {
		return "", err
	}
	switch f.Type {
	case TypeOpenStreamOK, TypeCloseStream:
		err := decodeObject(f.Payload, &struct{}{}, nil, nil, nil)
		return "", err
	case TypeOpenStreamError, TypeResetStream:
		var value StreamError
		keys := []string{"code"}
		if err := decodeObject(f.Payload, &value, keys, keys, keys); err != nil {
			return "", err
		}
		if !validStreamCode(value.Code) {
			return "", ErrInvalidHandshake
		}
		return value.Code, nil
	}
	return "", ErrUnexpectedType
}
