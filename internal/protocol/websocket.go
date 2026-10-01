package protocol

import (
	"encoding/base64"
	"net/http"
	"strings"
)

// HeaderHasToken compares complete HTTP tokens, never substrings.
func HeaderHasToken(h http.Header, name, token string) bool {
	for _, value := range h.Values(name) {
		for _, part := range strings.Split(value, ",") {
			if strings.EqualFold(strings.TrimSpace(part), token) {
				return true
			}
		}
	}
	return false
}

func WebSocketProtocols(h http.Header) ([]string, bool) {
	var result []string
	seen := make(map[string]bool)
	for _, value := range h.Values("Sec-WebSocket-Protocol") {
		for _, part := range strings.Split(value, ",") {
			part = strings.TrimSpace(part)
			if !HTTPToken(part) || seen[part] || len(result) >= 128 {
				return nil, false
			}
			seen[part] = true
			result = append(result, part)
		}
	}
	return result, true
}

func ValidWebSocketMetadata(h http.Header) bool {
	keys, versions := h.Values("Sec-WebSocket-Key"), h.Values("Sec-WebSocket-Version")
	if len(keys) != 1 || len(keys[0]) != 24 || len(versions) != 1 || versions[0] != "13" {
		return false
	}
	decoded, err := base64.StdEncoding.Strict().DecodeString(keys[0])
	if err != nil || len(decoded) != 16 || base64.StdEncoding.EncodeToString(decoded) != keys[0] {
		return false
	}
	_, valid := WebSocketProtocols(h)
	return valid && len(h.Values("Sec-WebSocket-Extensions")) == 0
}
