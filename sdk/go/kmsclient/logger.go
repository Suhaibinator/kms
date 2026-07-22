package kmsclient

import "log"

// Logger is the minimal logging surface the SDK uses. It mirrors the
// signature of the standard library's log.Printf.
//
// The SDK only ever logs operational information (paths, env-var names,
// connection state, revisions). It never logs secret plaintext.
type Logger interface {
	Printf(format string, args ...any)
}

// stdLogger is the default Logger, delegating to the standard log package.
type stdLogger struct{}

func (stdLogger) Printf(format string, args ...any) { log.Printf(format, args...) }
