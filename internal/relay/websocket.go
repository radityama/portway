package relay

import (
	"bufio"
	"io"
	"net/http"
	"time"

	"github.com/radityama/portway/internal/httpwire"
	"github.com/radityama/portway/internal/mux"
	"github.com/radityama/portway/internal/protocol"
)

func (s *Server) serveWebSocket(w http.ResponseWriter, request *http.Request, stream *mux.Stream, headers http.Header) {
	defer stream.Close()
	defer request.Body.Close()
	controller := http.NewResponseController(w)
	defer controller.SetWriteDeadline(time.Time{})
	response, reader, err := httpwire.ReadResponseUpgrade(bufio.NewReader(stream), request)
	if err != nil {
		_ = controller.SetWriteDeadline(time.Now().Add(s.WriteTimeout))
		httpFailure(w, "upstream upgrade unavailable", streamStatus(stream.Context(), err))
		return
	}
	defer response.Body.Close()
	if response.StatusCode != 101 {
		// Rejected upgrades remain ordinary HTTP; send request FIN so the
		// agent can release the stream after its response direction finishes.
		_ = stream.CloseWrite()
		if response.ContentLength > protocol.MaxResponseBodySize {
			httpFailure(w, "upstream response exceeds limit", 502)
			return
		}
		httpwire.StripHopHeaders(response.Header)
		for key, values := range response.Header {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		_ = controller.SetWriteDeadline(time.Now().Add(s.StreamTimeout))
		w.WriteHeader(response.StatusCode)
		if controller.Flush() != nil {
			return
		}
		body := &httpwire.LimitedBody{Reader: response.Body, Remaining: protocol.MaxResponseBodySize}
		buffer := make([]byte, protocol.MaxDataSize)
		for {
			n, readErr := body.Read(buffer)
			if n > 0 {
				_ = controller.SetWriteDeadline(time.Now().Add(s.StreamTimeout))
				if _, err := w.Write(buffer[:n]); err != nil {
					return
				}
				if controller.Flush() != nil {
					return
				}
			}
			if readErr == io.EOF {
				return
			}
			if readErr != nil {
				panic(http.ErrAbortHandler)
			}
		}
	}
	if !httpwire.ValidWebSocketResponse(response, headers) {
		stream.Reset(protocol.StreamInvalid)
		_ = controller.SetWriteDeadline(time.Now().Add(s.WriteTimeout))
		httpFailure(w, "invalid upstream upgrade", 502)
		return
	}
	conn, buffered, err := controller.Hijack()
	if err != nil {
		return
	}
	defer conn.Close()
	// Hijack preserves pipelined frames, but its reader still reads the raw
	// connection. Consume only its buffered bytes, then use the idle wrapper.
	_ = conn.SetDeadline(time.Time{})
	bounded := &httpwire.IdleConn{Conn: conn, Context: stream.Context(), Timeout: s.StreamTimeout}
	if err := httpwire.WriteWebSocketResponse(bounded, response.Header); err != nil {
		return
	}
	input := io.MultiReader(io.LimitReader(buffered.Reader, int64(buffered.Reader.Buffered())), bounded)
	httpwire.Bridge(stream, bounded, input, reader)
}
