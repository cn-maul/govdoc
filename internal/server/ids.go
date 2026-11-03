package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"sync/atomic"
	"time"
)

// newID returns a short random identifier for documents, jobs and files.
//
// Random rather than sequential on purpose: these ids appear in URLs the
// browser fetches, and a sequential id would let any local process enumerate
// every document the user has handled in this session by counting. For 公文,
// which may be 涉密, that is worth the few extra bytes.
func newID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand failing is not a recoverable condition, and falling
		// back to a predictable id would silently weaken the property above.
		// Use the counter form so the server keeps working, but make it
		// obvious in the value that it is a degraded id.
		return "fallback-" + itoa(atomic.AddInt64(&idCounter, 1))
	}
	return hex.EncodeToString(b[:])
}

var idCounter int64

// itoa is a small dependency-free integer formatter.
func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// analysisTimeout bounds one analysis when the client's own deadline is
// unknown.
const analysisTimeout = 10 * time.Minute

// analysisContext returns a context detached from the request that started
// the analysis, but still bounded, together with its cancel function.
//
// The analysis outlives its POST by design: the POST returns a job id
// immediately and the client follows the SSE stream. If the work were bound
// to the request context, a browser that navigated away — or a proxy that
// closed the (already answered) connection — would cancel the analysis, and
// the user would come back to a job that never finishes.
//
// The bounded timeout keeps that independence from becoming a leak: an
// analysis that hangs upstream is still reclaimed. The caller must call
// cancel (normally with defer) to release the timer.
func analysisContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), analysisTimeout)
}
