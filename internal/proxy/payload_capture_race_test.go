package proxy

import (
	"io"
	"sync"
	"testing"
	"time"
)

// pacedReader hands out a fixed-size chunk per Read with a pause in between, so
// a body of a few hundred kilobytes takes long enough for the reader below to
// take a real snapshot while the copy is still moving.
type pacedReader struct {
	data  []byte
	off   int
	chunk int
	pause time.Duration
}

func (r *pacedReader) Read(p []byte) (int, error) {
	if r.off >= len(r.data) {
		return 0, io.EOF
	}
	if r.pause > 0 {
		time.Sleep(r.pause)
	}
	n := copy(p, r.data[r.off:min(r.off+r.chunk, len(r.data))])
	r.off += n
	return n, nil
}

// TestPayloadCaptureReadCloser_StaysConsistentWhileTheBodyIsStillRead drives the
// payload-capture wrapper the way the data plane does.
//
// The wrapper is the inner half of the request body (reverse.go wraps r.Body
// with the payload capture and the counting wrapper goes outside it) and of the
// response body (ModifyResponse), and net/http reads both from a goroutine the
// handler does not own: the transport's write loop keeps reading a request body
// after the round trip returned - an upstream that answers 401/413 early, or a
// client that goes away - and httputil copies a response body while the handler
// unwinds. requestLifecycle.finish reads Payload/TotalLen/Truncated from the
// handler's goroutine on exactly that path, which is why the sibling wrappers in
// the same file count with atomics (countingReadCloser, countingReadWriteCloser).
//
// With a plain bytes.Buffer and a plain int the two goroutines race: -race
// reports it, and a caller can additionally reach into the buffer the reader is
// still appending to, so a request log entry can carry a body that was never
// read. Run with -race.
func TestPayloadCaptureReadCloser_StaysConsistentWhileTheBodyIsStillRead(t *testing.T) {
	const (
		bodyLen  = 512 * 1024
		maxBytes = 64 * 1024
	)
	// The body is its own offset pattern, so a snapshot that is not the prefix
	// that was actually read is a wrong byte and not just a short slice.
	body := make([]byte, bodyLen)
	for i := range body {
		body[i] = byte(i)
	}
	source := &pacedReader{data: body, chunk: 4 * 1024, pause: 100 * time.Microsecond}
	capture := newPayloadCaptureReadCloser(io.NopCloser(source), maxBytes)

	copied := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(copied)
		// The transport's write loop: it reads the body in its own goroutine.
		if _, err := io.Copy(io.Discard, capture); err != nil {
			t.Errorf("copy the body: %v", err)
		}
	}()

	// A snapshot is read in this order: payload, total, flag. The three reads
	// are not one atomic observation - the body is still moving - so only the
	// relations that hold for any interleaving are asserted here: the payload is
	// always the prefix that was read, it never grows past the cap, and it never
	// holds more than the total it was taken from.
	lastLen := 0
	snapshots := 0
loop:
	for {
		payload := capture.Payload()
		total := capture.TotalLen()
		truncated := capture.Truncated()
		snapshots++

		if len(payload) < lastLen {
			t.Fatalf("Payload went backwards: %d bytes then %d", lastLen, len(payload))
		}
		lastLen = len(payload)
		for i, b := range payload {
			if b != byte(i) {
				t.Fatalf("payload[%d] = %d, want %d: the snapshot is not the prefix that was read",
					i, b, byte(i))
			}
		}
		if len(payload) > total {
			t.Fatalf("the payload holds %d bytes but TotalLen is %d", len(payload), total)
		}
		if len(payload) > maxBytes {
			t.Fatalf("the payload holds %d bytes, past the %d-byte cap", len(payload), maxBytes)
		}
		if truncated && total <= len(payload) {
			t.Fatalf("Truncated() is true with TotalLen %d and %d captured bytes: the flag "+
				"contradicts the counters", total, len(payload))
		}

		select {
		case <-copied:
			break loop
		default:
		}
	}
	wg.Wait()

	// Once the copy is over the snapshot is unambiguous: everything was read,
	// the capture stopped at its cap, and the flag says so.
	final := capture.Payload()
	if got := capture.TotalLen(); got != bodyLen {
		t.Fatalf("TotalLen after the whole body = %d, want %d", got, bodyLen)
	}
	if len(final) != maxBytes {
		t.Fatalf("the captured payload is %d bytes, want the %d-byte cap", len(final), maxBytes)
	}
	if !capture.Truncated() {
		t.Fatal("Truncated() is false after a body longer than the cap")
	}
	if final[0] != body[0] || final[maxBytes-1] != body[maxBytes-1] {
		t.Fatal("the captured payload does not hold the bytes that were read")
	}

	// Payload hands out a copy: the reader may append to the buffer after it
	// returned, and a caller must not be able to reach into the capture either.
	final[0] ^= 0xff
	if again := capture.Payload(); again[0] != body[0] {
		t.Fatal("Payload() handed out the internal buffer: a caller can corrupt the captured body")
	}
	if snapshots < 2 {
		t.Fatalf("only %d snapshot(s) were taken, the test did not overlap the two goroutines", snapshots)
	}
}
