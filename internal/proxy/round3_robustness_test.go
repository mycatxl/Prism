package proxy

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	M "github.com/sagernet/sing/common/metadata"
)

func TestRound3ProxyRefusalDoesNotDrainStalledUpload(t *testing.T) {
	for _, method := range []string{http.MethodPost, http.MethodConnect} {
		t.Run(method, func(t *testing.T) {
			server := httptest.NewServer(NewForwardProxy(ForwardProxyConfig{}))
			defer server.Close()
			conn, err := net.Dial("tcp", server.Listener.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
			target := "http://example.com/"
			if method == http.MethodConnect {
				target = "example.com:443"
			}
			_, _ = fmt.Fprintf(conn, "%s %s HTTP/1.1\r\nHost: example.com\r\nContent-Length: 1\r\n\r\n", method, target)
			resp, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: method})
			if err != nil {
				t.Fatalf("refusal waited for unfinished upload: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusProxyAuthRequired || !resp.Close {
				t.Fatalf("status=%d close=%v", resp.StatusCode, resp.Close)
			}
			if _, err := io.ReadAll(resp.Body); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestRound3InvalidForwardAuthoritiesNeverDial(t *testing.T) {
	for _, method := range []string{http.MethodConnect, http.MethodGet} {
		for _, authority := range []string{"example.com:65536", "example.com:65561", "example.com:999999999999999999999999", "example.com:"} {
			t.Run(method+"/"+authority, func(t *testing.T) {
				env := newProxyE2EEnv(t)
				var dials atomic.Int32
				setProxyE2EOutboundDialFunc(t, env, func(context.Context, string, M.Socksaddr) (net.Conn, error) {
					dials.Add(1)
					return nil, fmt.Errorf("unexpected dial")
				})
				fp, _, _, registry := newQuotaForwardProxy(t, env, &mutableKeyStore{state: workingKeyState()})
				target := authority
				if method == http.MethodGet {
					target = "http://" + authority + "/"
				}
				raw := fmt.Sprintf("%s %s HTTP/1.1\r\nHost: %s\r\nProxy-Authorization: %s\r\n\r\n", method, target, authority, basicAuth("plat", testCredential))
				req, err := http.ReadRequest(bufio.NewReader(strings.NewReader(raw)))
				if err != nil {
					t.Fatal(err)
				}
				rr := httptest.NewRecorder()
				fp.ServeHTTP(rr, req)
				if rr.Code != 400 || dials.Load() != 0 {
					t.Fatalf("status=%d dials=%d", rr.Code, dials.Load())
				}
				waitForSessionCount(t, registry, workingKeyState().KeyID, 0)
			})
		}
	}
}

func TestRound3ForwardPreservesTruncatedResponseFailure(t *testing.T) {
	for _, framing := range []string{"Transfer-Encoding: chunked\r\n\r\n5\r\nhello\r\n", "Content-Length: 10\r\n\r\nhello"} {
		t.Run(strings.Split(framing, ":")[0], func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				conn, rw, err := http.NewResponseController(w).Hijack()
				if err != nil {
					return
				}
				defer conn.Close()
				_, _ = rw.WriteString("HTTP/1.1 200 OK\r\n" + framing)
				_ = rw.Flush()
			}))
			defer upstream.Close()
			env := newProxyE2EEnv(t)
			setProxyE2EOutboundDialFunc(t, env, func(context.Context, string, M.Socksaddr) (net.Conn, error) {
				return net.Dial("tcp", upstream.Listener.Addr().String())
			})
			fp, events, _, registry := newQuotaForwardProxy(t, env, &mutableKeyStore{state: workingKeyState()})
			server := httptest.NewServer(fp)
			defer server.Close()
			proxyURL, _ := url.Parse(server.URL)
			proxyURL.User = url.UserPassword("plat", testCredential)
			client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}, Timeout: 2 * time.Second}
			defer client.CloseIdleConnections()
			resp, err := client.Get(upstream.URL)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			if err == nil || string(body) != "hello" {
				t.Fatalf("truncated response became complete: body=%q err=%v", body, err)
			}
			waitForRequestLog(t, events)
			waitForSessionCount(t, registry, workingKeyState().KeyID, 0)
		})
	}
}

func TestRound3ConnectHijackFailureDoesNotPenalizeNode(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		env := newProxyE2EEnv(t)
		upstream, peer := net.Pipe()
		defer peer.Close()
		setProxyE2EOutboundDialFunc(t, env, func(context.Context, string, M.Socksaddr) (net.Conn, error) { return upstream, nil })
		fp, _, _, registry := newQuotaForwardProxy(t, env, &mutableKeyStore{state: workingKeyState()})
		health := &mockHealthRecorder{}
		fp.health = health
		req := httptest.NewRequest(http.MethodConnect, "example.com:443", nil)
		req.Host = "example.com:443"
		req.Header.Set("Proxy-Authorization", basicAuth("plat", testCredential))
		rr := httptest.NewRecorder()
		fp.ServeHTTP(rr, req)
		if rr.Code != http.StatusBadGateway {
			t.Fatalf("did not reach hijack: status=%d body=%s", rr.Code, rr.Body.String())
		}
		synctest.Wait()
		if health.resultCalls.Load() != 0 {
			t.Fatal("downstream hijack failure changed upstream health")
		}
		if _, err := peer.Read(make([]byte, 1)); err != io.EOF {
			t.Fatalf("upstream not closed: %v", err)
		}
		waitForSessionCount(t, registry, workingKeyState().KeyID, 0)
	})
}
