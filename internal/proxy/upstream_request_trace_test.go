package proxy

import (
	"errors"
	"net/http/httptrace"
	"testing"
)

func TestUpstreamRequestTrace_NoConnNoWrite(t *testing.T) {
	trace := newUpstreamRequestTrace()
	if trace.shouldCommitEgress() {
		t.Fatal("shouldCommitEgress: got true, want false")
	}
}

func TestUpstreamRequestTrace_GotConnAndSuccessfulWrite(t *testing.T) {
	trace := newUpstreamRequestTrace()
	clientTrace := trace.clientTrace()
	clientTrace.GotConn(httptrace.GotConnInfo{})
	clientTrace.WroteRequest(httptrace.WroteRequestInfo{Err: nil})

	if !trace.shouldCommitEgress() {
		t.Fatal("shouldCommitEgress: got false, want true")
	}
}

func TestUpstreamRequestTrace_GotConnButWriteFailed(t *testing.T) {
	trace := newUpstreamRequestTrace()
	clientTrace := trace.clientTrace()
	clientTrace.GotConn(httptrace.GotConnInfo{})
	clientTrace.WroteRequest(httptrace.WroteRequestInfo{Err: errors.New("write failed")})

	if trace.shouldCommitEgress() {
		t.Fatal("shouldCommitEgress: got true, want false")
	}
}

func TestUpstreamRequestTrace_RetryAfterWriteFailure(t *testing.T) {
	trace := newUpstreamRequestTrace()
	clientTrace := trace.clientTrace()
	clientTrace.GotConn(httptrace.GotConnInfo{})
	clientTrace.WroteRequest(httptrace.WroteRequestInfo{Err: errors.New("first attempt failed")})
	clientTrace.WroteRequest(httptrace.WroteRequestInfo{Err: nil})

	if !trace.shouldCommitEgress() {
		t.Fatal("shouldCommitEgress: got false, want true")
	}
}
