package api

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"runtime/debug"
)

// RequestIDHeader carries the identifier back to the caller, and is honoured
// when the caller sets it — a client with its own tracing already has an id,
// and inventing a second one for the same request helps nobody.
const RequestIDHeader = "X-Request-Id"

type requestIDKey struct{}

// requestIDFrom returns the id attached to a request, or empty.
func requestIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey{}).(string)
	return id
}

// withRequestID gives every request an identifier, echoes it, and puts it in
// the context so log lines can carry it.
//
// Without one, "solve failed" in the log cannot be tied to the caller who saw
// it. Two requests for the same url a second apart are otherwise identical in
// the log, and that is the pair someone always asks about.
func withRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(RequestIDHeader)
		if id == "" {
			var b [8]byte
			// rand.Read from crypto/rand cannot fail; it panics instead.
			rand.Read(b[:])
			id = hex.EncodeToString(b[:])
		}
		w.Header().Set(RequestIDHeader, id)
		next.ServeHTTP(w, r.WithContext(
			context.WithValue(r.Context(), requestIDKey{}, id)))
	})
}

// recovered turns a panic into a 500 and a log line.
//
// net/http already stops a panicking handler from taking the process with it,
// but what the caller gets is a closed connection with no status and no body,
// which is indistinguishable from the network failing. The stack goes to the
// structured logger rather than to stderr, so it lands wherever the rest of
// the logs do.
func recovered(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			v := recover()
			if v == nil {
				return
			}
			// A client that hung up mid-response makes net/http panic with
			// ErrAbortHandler on purpose, and it is not an error to report.
			if v == http.ErrAbortHandler {
				panic(v)
			}
			log.Error("panic serving request",
				"request_id", requestIDFrom(r.Context()),
				"path", r.URL.Path,
				"panic", v,
				"stack", string(debug.Stack()))
			writeFailure(w, ReasonInternal, "internal error")
		}()
		next.ServeHTTP(w, r)
	})
}
