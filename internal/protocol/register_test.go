package protocol

import (
	"encoding/hex"
	"errors"
	"strings"
	"testing"
)

func TestRegistrationGoldenPayloads(t *testing.T) {
	for _, fixture := range loadFixtures(t).Frames {
		payload, _ := hex.DecodeString(fixture.PayloadHex)
		frame := Frame{Version: 1, Type: Type(fixture.Type), Payload: payload}
		var canonical Frame
		var err error
		switch frame.Type {
		case TypeRegister:
			value, decodeErr := DecodeRegister(frame)
			if decodeErr != nil || value.Generation != Generation(^uint64(0)) {
				t.Fatal("invalid full-width registration fixture")
			}
			canonical, err = EncodeRegister(value)
		case TypeRegisterOK:
			value, decodeErr := DecodeRegisterOK(frame)
			if decodeErr != nil || value.Generation != Generation(^uint64(0)) {
				t.Fatal("invalid registration ACK fixture")
			}
			canonical, err = EncodeRegisterOK(value)
		case TypeRegisterError:
			value, decodeErr := DecodeRegisterError(frame)
			if decodeErr != nil {
				t.Fatal(decodeErr)
			}
			canonical, err = EncodeRegisterError(value)
		default:
			continue
		}
		if err != nil || string(canonical.Payload) != string(payload) {
			t.Fatalf("noncanonical registration fixture %s", fixture.Name)
		}
	}
}

func TestStrictRegisterPayloads(t *testing.T) {
	valid := `{"tunnel_id":"tnl_fixture","generation":"1"}`
	bad := []string{
		`{}`, `null`, `[]`, valid + `{}`, `{"Tunnel_id":"tnl_fixture","generation":"1"}`,
		`{"tunnel_id":"tnl_fixture","generation":"1","hostname":"victim.example.com"}`,
		`{"tunnel_id":"tnl_fixture","generation":"1","generation":"2"}`,
		`{"tunnel_id":null,"generation":"1"}`, `{"tunnel_id":"bad/id","generation":"1"}`,
		`{"tunnel_id":"tnl_fixture","generation":1}`, `{"tunnel_id":"tnl_fixture","generation":null}`,
	}
	for _, generation := range []string{"0", "01", "-1", "+1", "1.0", "1e2", "18446744073709551616", "", " 1", "１"} {
		bad = append(bad, `{"tunnel_id":"tnl_fixture","generation":"`+generation+`"}`)
	}
	for _, payload := range bad {
		if _, err := DecodeRegister(Frame{Version: 1, Type: TypeRegister, Payload: []byte(payload)}); err == nil {
			t.Fatalf("accepted malformed registration: %s", payload)
		}
	}
	for _, id := range []string{"a", strings.Repeat("a", MaxTunnelIDSize), "Case-Sensitive_ID"} {
		if _, err := EncodeRegister(Register{TunnelID: id, Generation: 1}); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"", strings.Repeat("a", 129), "bad@id", "bad.id", "é"} {
		if _, err := EncodeRegister(Register{TunnelID: id, Generation: 1}); err == nil {
			t.Fatal("invalid tunnel ID accepted")
		}
	}
	for _, typ := range []Type{TypeRegister, TypeRegisterOK, TypeRegisterError} {
		header := validDataHeader(MaxHandshakePayloadSize + 1)
		header[1] = byte(typ)
		clear(header[4:12])
		reader := &headerReader{header: header}
		if _, err := Decode(reader); !errors.Is(err, ErrPayloadTooLarge) || reader.bodyReads != 0 {
			t.Fatal("registration allocated oversized body")
		}
	}
}

func TestRegisterACKBindingAndErrors(t *testing.T) {
	request := Register{TunnelID: "tnl_fixture", Generation: 17}
	id := "con_0123456789abcdef0123456789abcdef"
	ack := RegisterOK{TunnelID: request.TunnelID, Generation: request.Generation, ConnectionID: id, PublicHostname: "p-abc.portway.localhost"}
	if err := ack.ValidateFor(request, id); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*RegisterOK){func(a *RegisterOK) { a.TunnelID = "tnl_other" }, func(a *RegisterOK) { a.ConnectionID = "con_aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa" }, func(a *RegisterOK) { a.Generation++ }, func(a *RegisterOK) { a.PublicHostname = "bad.example:443" }} {
		copy := ack
		mutate(&copy)
		if err := copy.ValidateFor(request, id); err == nil {
			t.Fatal("ACK binding mismatch accepted")
		}
	}
	for _, host := range []string{"UPPER.example", "127.0.0.1", "[::1]", "a.example.", "a..example", "-a.example", "a.example/path", "a@b.example", strings.Repeat("a", 64) + ".example"} {
		if ValidHostname(host) {
			t.Fatal("unsafe hostname accepted")
		}
	}
	for _, code := range []string{RegisterInvalid, RegisterForbidden, RegisterStale, RegisterCapacity, RegisterConflict} {
		frame, err := EncodeRegisterError(RegisterError{Code: code})
		if err != nil {
			t.Fatal(err)
		}
		value, err := DecodeRegisterError(frame)
		if err != nil || value.Code != code {
			t.Fatal("registration code mismatch")
		}
	}
	for _, payload := range []string{`{"code":"unknown"}`, `{"code":null}`, `{"code":"REGISTER_STALE","tunnel_id":"secret"}`, `{"code":"REGISTER_STALE","code":"REGISTER_INVALID"}`, `{"Code":"REGISTER_STALE"}`} {
		if _, err := DecodeRegisterError(Frame{Version: 1, Type: TypeRegisterError, Payload: []byte(payload)}); err == nil {
			t.Fatal("malformed registration error accepted")
		}
	}
}
