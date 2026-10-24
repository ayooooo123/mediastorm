package handlers

import (
	"errors"
	"io"
	"time"
)

// liveDirectFirstByteTimeout bounds how long a direct live stream may stay silent before its
// response starts. Once a 200 is written the client can only wait: a player opening the stream
// blocks in its demuxer probe, and a provider that accepts the connection but never sends (a full
// connection limit, a dead channel) kept it there for as long as the connection lasted. Holding
// the status until the first bytes turns that into an error the clients already recover from
// (the direct → HLS fallback).
var liveDirectFirstByteTimeout = 20 * time.Second

var errLiveFirstByteTimeout = errors.New("no data from the live stream before the first-byte deadline")

// liveDirectFirstByteDeadline adds FFmpeg's configured probe on the transmux path, which
// reads that much input before it writes anything.
func liveDirectFirstByteDeadline(analyzeDurationSec int) time.Duration {
	return liveDirectFirstByteTimeout + time.Duration(max(analyzeDurationSec, 0))*time.Second
}

// readFirstLiveChunk waits up to timeout for the first read from src into buf. On timeout it
// calls abort, which must unblock the pending read (kill the process, close the body), and
// waits for that read to return before reporting errLiveFirstByteTimeout, so buf is never
// written after this returns.
func readFirstLiveChunk(src io.Reader, buf []byte, timeout time.Duration, abort func()) (int, error) {
	type result struct {
		n   int
		err error
	}
	done := make(chan result, 1)
	go func() {
		n, err := src.Read(buf)
		done <- result{n, err}
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case res := <-done:
		return res.n, res.err
	case <-timer.C:
		abort()
		<-done
		return 0, errLiveFirstByteTimeout
	}
}
