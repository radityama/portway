package httpwire

import (
	"crypto/sha1"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"

	"github.com/radityama/portway/internal/protocol"
)

func WebSocketRequest(r *http.Request) bool {
	h := r.Header.Clone()
	h.Del("Sec-WebSocket-Extensions") // Compression is deliberately not negotiated.
	return r.Method == "GET" && r.ProtoMajor == 1 && r.ProtoMinor == 1 && r.ContentLength == 0 && len(r.TransferEncoding) == 0 && len(r.Trailer) == 0 && len(h.Values("Trailer")) == 0 && len(h.Values("Upgrade")) == 1 && strings.EqualFold(h.Get("Upgrade"), "websocket") && validUpgradeConnection(h) && protocol.ValidWebSocketMetadata(h)
}

func validUpgradeConnection(h http.Header) bool {
	if !protocol.HeaderHasToken(h, "Connection", "upgrade") {
		return false
	}
	for _, value := range h.Values("Connection") {
		for _, token := range strings.Split(value, ",") {
			if !protocol.HTTPToken(strings.TrimSpace(token)) {
				return false
			}
		}
	}
	return true
}

func WebSocketAccept(key string) string {
	digest := sha1.Sum([]byte(key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
	return base64.StdEncoding.EncodeToString(digest[:])
}

func ValidWebSocketResponse(r *http.Response, request http.Header) bool {
	h := r.Header
	selected, valid := protocol.WebSocketProtocols(h)
	offered, _ := protocol.WebSocketProtocols(request)
	length := h.Get("Content-Length")
	if r.StatusCode != 101 || r.ProtoMajor != 1 || r.ProtoMinor != 1 || len(h.Values("Upgrade")) != 1 || !strings.EqualFold(h.Get("Upgrade"), "websocket") || !validUpgradeConnection(h) || len(h.Values("Sec-WebSocket-Accept")) != 1 || h.Get("Sec-WebSocket-Accept") != WebSocketAccept(request.Get("Sec-WebSocket-Key")) || !valid || len(selected) > 1 || len(selected) == 1 && !slices.Contains(offered, selected[0]) || len(h.Values("Sec-WebSocket-Extensions")) != 0 || len(r.TransferEncoding) != 0 || len(r.Trailer) != 0 || length != "" && length != "0" {
		return false
	}
	// Connection-nominated application handshake headers must survive stripping.
	clean := h.Clone()
	StripHopHeaders(clean)
	return clean.Get("Sec-WebSocket-Accept") == h.Get("Sec-WebSocket-Accept") && clean.Get("Sec-WebSocket-Protocol") == h.Get("Sec-WebSocket-Protocol")
}

func WriteWebSocketResponse(w io.Writer, h http.Header) error {
	h = h.Clone()
	StripHopHeaders(h)
	h.Set("Connection", "Upgrade")
	h.Set("Upgrade", "websocket")
	if _, err := fmt.Fprint(w, "HTTP/1.1 101 Switching Protocols\r\n"); err != nil {
		return err
	}
	if err := h.Write(w); err != nil {
		return err
	}
	_, err := io.WriteString(w, "\r\n")
	return err
}
