package protocol

import "errors"

const GoAwayShutdown = "SHUTDOWN"
const GoAwayDrained = "DRAINED"

var ErrInvalidGoAway = errors.New("invalid GOAWAY payload")

type GoAway struct {
	Code string `json:"code"`
}

func EncodeGoAway(value GoAway) (Frame, error) {
	if value.Code != GoAwayShutdown && value.Code != GoAwayDrained {
		return Frame{}, ErrInvalidGoAway
	}
	return encodeHandshake(TypeGoAway, value)
}
func DecodeGoAway(frame Frame) (GoAway, error) {
	if err := validateHandshakeFrame(frame, TypeGoAway); err != nil {
		return GoAway{}, err
	}
	var value GoAway
	keys := []string{"code"}
	if err := decodeObject(frame.Payload, &value, keys, keys, keys); err != nil {
		return GoAway{}, ErrInvalidGoAway
	}
	if value.Code != GoAwayShutdown && value.Code != GoAwayDrained {
		return GoAway{}, ErrInvalidGoAway
	}
	return value, nil
}
