package yamlls

import "io"

// readWriteCloseSubprocess adapts the subprocess stdin/stdout pair into a
// single io.ReadWriteCloser for jsonrpc2.NewStream. Reads come from stdout,
// writes go to stdin, Close closes stdin (signalling EOF to the subprocess).
type readWriteCloseSubprocess struct {
	stdout io.ReadCloser
	stdin  io.WriteCloser
}

func (p readWriteCloseSubprocess) Read(b []byte) (int, error) {
	return p.stdout.Read(b)
}

func (p readWriteCloseSubprocess) Write(b []byte) (int, error) {
	return p.stdin.Write(b)
}

func (p readWriteCloseSubprocess) Close() error {
	return p.stdin.Close()
}
