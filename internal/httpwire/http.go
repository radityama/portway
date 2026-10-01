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
	response, _, err := readResponse(reader, request, false)
	return response, err
}

// ReadResponseUpgrade also returns the exact reader owning buffered post-header
// bytes. A 101 is permitted here only; callers must validate its handshake.
func ReadResponseUpgrade(reader *bufio.Reader, request *http.Request) (*http.Response, io.Reader, error) {
	return readResponse(reader, request, true)
}

func readResponse(reader *bufio.Reader, request *http.Request, upgrade bool) (*http.Response, io.Reader, error) {
	for n := 0; n < 8; n++ {
		var header []byte
		for {
			part, err := reader.ReadSlice('\n')
			if len(header)+len(part) > protocol.MaxHTTPHeaderSize {
				return nil, nil, ErrHeaders
			}
			header = append(header, part...)
			if errors.Is(err, bufio.ErrBufferFull) {
				continue
			}
			if err != nil {
				return nil, nil, err
			}
			if bytes.HasSuffix(header, []byte("\r\n\r\n")) {
				break
			}
		}
		parsed := bufio.NewReader(io.MultiReader(bytes.NewReader(header), reader))
		response, err := http.ReadResponse(parsed, request)
		if err != nil {
			return nil, nil, ErrHeaders
		}
		if len(response.Trailer) > 0 || len(response.Header.Values("Trailer")) > 0 || response.StatusCode == http.StatusSwitchingProtocols && !upgrade {
			return nil, nil, ErrHeaders
		}
		if response.StatusCode == http.StatusSwitchingProtocols {
			return response, parsed, nil
		}
		if response.StatusCode < 200 {
			response.Body.Close()
			continue
		}
		return response, parsed, nil
	}
	return nil, nil, ErrHeaders
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
