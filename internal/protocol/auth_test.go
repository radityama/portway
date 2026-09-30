package protocol

import (
	"bytes"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestAuthWireFixtures(t *testing.T) {
	for _, fixture := range loadFixtures(t).Frames {
		if fixture.Type < 3 || fixture.Type > 5 {
			continue
		}
		wire, err := hex.DecodeString(fixture.WireHex)
		if err != nil {
			t.Fatal(err)
		}
		frame, err := Decode(bytes.NewReader(wire))
		if err != nil {
			t.Fatal(err)
		}
		var encoded Frame
		switch frame.Type {
		case TypeAuth:
			value, decodeErr := DecodeAuth(frame)
			if decodeErr != nil {
				t.Fatal(decodeErr)
			}
			encoded, err = EncodeAuth(value)
		case TypeAuthOK:
			value, decodeErr := DecodeAuthOK(frame)
			if decodeErr != nil {
				t.Fatal(decodeErr)
			}
			encoded, err = EncodeAuthOK(value)
		case TypeAuthError:
			value, decodeErr := DecodeAuthError(frame)
			if decodeErr != nil {
				t.Fatal(decodeErr)
			}
			encoded, err = EncodeAuthError(value)
		}
		if err != nil {
			t.Fatal(err)
		}
		assertFrameEqual(t, encoded, frame)
	}
}
func TestAuthTokenAndPayloadValidation(t *testing.T) {
	for _, token := range []string{strings.Repeat("a", 32), strings.Repeat("a", 512)} {
		frame, err := EncodeAuth(Auth{Token: token})
		if err != nil {
			t.Fatal(err)
		}
		value, err := DecodeAuth(frame)
		if err != nil || value.Token != token {
			t.Fatal("token boundary failed")
		}
	}
	for _, token := range []string{"", strings.Repeat("a", 31), strings.Repeat("a", 513), strings.Repeat("a", 32) + " ", strings.Repeat("a", 32) + "\n", strings.Repeat("a", 32) + "é"} {
		if _, err := EncodeAuth(Auth{Token: token}); err == nil {
			t.Fatal("invalid token encoded")
		}
	}
	for _, payload := range []string{`null`, `{}`, `{"token":null}`, `{"token":123}`, `{"token":"short"}`, `{"token":"fixture_credential_only_for_tests","token":"fixture_credential_only_for_tests"}`, `{"token":"fixture_credential_only_for_tests","extra":"never_log_this"}`, `{"Token":"fixture_credential_only_for_tests"}`, `{"token":"fixture_credential_only_for_tests"} {}`} {
		if _, err := DecodeAuth(Frame{Version: 1, Type: TypeAuth, Payload: []byte(payload)}); err == nil || strings.Contains(err.Error(), "never_log_this") {
			t.Fatal("malformed AUTH was accepted or disclosed payload")
		}
	}
}
func TestAuthAcknowledgementValidation(t *testing.T) {
	valid := AuthOK{ConnectionID: "con_0123456789abcdef0123456789abcdef", ExpiresAt: time.Now().Add(time.Hour)}
	for _, mutate := range []func(*AuthOK){func(a *AuthOK) { a.ConnectionID = "" }, func(a *AuthOK) { a.ConnectionID = "con_0123456789abcdef0123456789abcdeg" }, func(a *AuthOK) { a.ExpiresAt = time.Time{} }} {
		value := valid
		mutate(&value)
		if _, err := EncodeAuthOK(value); err == nil {
			t.Fatal("invalid AUTH_OK encoded")
		}
	}
	for _, payload := range []string{`{}`, `{"connection_id":null,"expires_at":null}`, `{"connection_id":"con_0123456789abcdef0123456789abcdef","expires_at":"invalid"}`, `{"connection_id":"con_0123456789abcdef0123456789abcdef","expires_at":"2026-10-01T00:00:00Z","extra":true}`} {
		if _, err := DecodeAuthOK(Frame{Version: 1, Type: TypeAuthOK, Payload: []byte(payload)}); err == nil {
			t.Fatal("malformed AUTH_OK accepted")
		}
	}
	for _, code := range []string{AuthInvalid, AuthExpired, AuthRevoked} {
		frame, err := EncodeAuthError(AuthError{Code: code})
		if err != nil {
			t.Fatal(err)
		}
		value, err := DecodeAuthError(frame)
		if err != nil || value.Code != code {
			t.Fatal("AUTH_ERROR round trip failed")
		}
	}
	if _, err := EncodeAuthError(AuthError{Code: "arbitrary"}); err == nil {
		t.Fatal("unknown authentication code accepted")
	}
	for _, payload := range []string{`{}`, `{"code":null}`, `{"code":"arbitrary"}`, `{"code":"AUTH_INVALID","message":"raw payload"}`} {
		if _, err := DecodeAuthError(Frame{Version: 1, Type: TypeAuthError, Payload: []byte(payload)}); err == nil {
			t.Fatal("malformed AUTH_ERROR accepted")
		}
	}
	if _, err := DecodeAuth(Frame{Version: 1, Type: TypeAuthOK}); !errors.Is(err, ErrInvalidHandshake) {
		t.Fatal("wrong control type accepted")
	}
}
func TestStateValidationBeforePayloadReads(t *testing.T) {
	for _, allowed := range [][]Type{{TypeHello}, {TypeAuth}, nil} {
		reader := &headerReader{header: validDataHeader(1)}
		if _, err := DecodeTypes(reader, MaxPayloadSize, allowed...); !errors.Is(err, ErrUnexpectedType) || reader.bodyReads != 0 {
			t.Fatal("state validation read unexpected body")
		}
	}
	reader := &headerReader{header: validDataHeader(1)}
	if _, err := DecodeTypes(reader, MaxPayloadSize, Type(0xff)); !errors.Is(err, ErrUnknownType) || reader.reads != 0 {
		t.Fatal("invalid allowed type performed I/O")
	}
	for _, frameType := range []Type{TypeAuth, TypeAuthOK, TypeAuthError} {
		header := validDataHeader(MaxHandshakePayloadSize + 1)
		header[1] = byte(frameType)
		clear(header[4:12])
		reader := &headerReader{header: header}
		if _, err := Decode(reader); !errors.Is(err, ErrPayloadTooLarge) || reader.bodyReads != 0 {
			t.Fatal("oversized authentication header read body")
		}
	}
}
