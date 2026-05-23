// Package lsptest provides test helpers for LSP wire-level tests.
//
// This file contains ReadMessage and WriteMessage — a copy of the
// Content-Length framing functions from internal/lsp/framing — so that
// integration tests can drive the LSP server over in-memory pipes without
// importing the framing package directly. The framing package will be deleted
// in Chunk F; the test helpers here let tests continue to work after that
// deletion.
package lsptest

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// ErrMalformedHeader is returned by [ReadMessage] when the header section of
// an LSP message does not conform to the Content-Length framing protocol.
var ErrMalformedHeader = errors.New("framing: malformed header")

// ReadMessage reads one Content-Length-framed LSP message from r and returns
// the raw JSON body bytes. It tolerates both CRLF and LF line endings in
// headers. Returns an error wrapping [ErrMalformedHeader] on invalid headers
// and io.EOF when the underlying reader is exhausted before a complete message
// has been received.
func ReadMessage(r *bufio.Reader) ([]byte, error) {
	var contentLength int
	sawContentLength := false

	// Read header lines until the blank line.
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			if err == io.EOF && line == "" {
				return nil, io.EOF
			}
			return nil, fmt.Errorf("framing: reading header: %w", err)
		}

		// Normalise: strip trailing \r\n or \n.
		line = strings.TrimRight(line, "\r\n")

		// A blank line signals the end of the header section.
		if line == "" {
			break
		}

		name, value, ok := strings.Cut(line, ":")
		if !ok {
			return nil, fmt.Errorf("%w: %q", ErrMalformedHeader, line)
		}
		name = strings.TrimSpace(name)
		value = strings.TrimSpace(value)

		if strings.EqualFold(name, "Content-Length") {
			n, err := strconv.Atoi(value)
			if err != nil || n < 0 {
				return nil, fmt.Errorf("%w: invalid Content-Length value %q", ErrMalformedHeader, value)
			}
			contentLength = n
			sawContentLength = true
		}
		// Unrecognised headers are silently ignored (per LSP spec).
	}

	if !sawContentLength {
		return nil, fmt.Errorf("%w: missing Content-Length header", ErrMalformedHeader)
	}

	body := make([]byte, contentLength)
	if _, err := io.ReadFull(r, body); err != nil {
		return nil, fmt.Errorf("framing: reading body: %w", err)
	}
	return body, nil
}

// WriteMessage writes body as a single Content-Length-framed LSP message to w,
// using CRLF line endings as required by the LSP specification.
// The caller is responsible for serialising concurrent writes (one writer
// goroutine is the intended pattern).
func WriteMessage(w io.Writer, body []byte) error {
	header := fmt.Sprintf("Content-Length: %d\r\n\r\n", len(body))
	if _, err := io.WriteString(w, header); err != nil {
		return fmt.Errorf("framing: writing header: %w", err)
	}
	if _, err := w.Write(body); err != nil {
		return fmt.Errorf("framing: writing body: %w", err)
	}
	return nil
}
