// Package httpwire bounds HTTP response parsing and strips transport headers.
package httpwire

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/radityama/portway/internal/protocol"
)

var ErrHeaders = errors.New("invalid or oversized upstream HTTP headers")
var ErrBody = errors.New("HTTP body exceeds limit")

func StripHopHeaders(h http.Header) {
	for _, connection := range h.Values("Connection") {
		for _, name := range strings.Split(connection, ",") {
			h.Del(strings.TrimSpace(name))
		}
	}
	for key := range h {
		if protocol.ForbiddenHeader(key) {
			h.Del(key)
		}
	}
}

// ReadResponse parses only bounded headers. Callers own and close the transport
// on errors; closing a rejected response body here could drain an untrusted body.
func ReadResponse(reader *bufio.Reader, request *http.Request) (*http.Response, error) {
	for n := 0; n < 8; n++ {
		var header []byte
		for {
			part, err := reader.ReadSlice('\n')
			if len(header)+len(part) > protocol.MaxHTTPHeaderSize {
				return nil, ErrHeaders
			}
			header = append(header, part...)
			if errors.Is(err, bufio.ErrBufferFull) {
				continue
			}
			if err != nil {
				return nil, err
			}
			if bytes.HasSuffix(header, []byte("\r\n\r\n")) {
				break
			}
		}
		response, err := http.ReadResponse(bufio.NewReader(io.MultiReader(bytes.NewReader(header), reader)), request)
		if err != nil {
			return nil, ErrHeaders
		}
		if response.StatusCode == http.StatusSwitchingProtocols || len(response.Trailer) > 0 {
			return nil, ErrHeaders
		}
		if response.StatusCode < 200 {
			response.Body.Close()
			continue
		}
		return response, nil
	}
	return nil, ErrHeaders
}

type LimitedBody struct {
	Reader    io.Reader
	Remaining int64
}

func (b *LimitedBody) Read(p []byte) (int, error) {
	if b.Remaining == 0 {
		var one [1]byte
		n, err := b.Reader.Read(one[:])
		if n > 0 {
			return 0, ErrBody
		}
		return 0, err
	}
	if int64(len(p)) > b.Remaining {
		p = p[:b.Remaining]
	}
	n, err := b.Reader.Read(p)
	b.Remaining -= int64(n)
	return n, err
}
