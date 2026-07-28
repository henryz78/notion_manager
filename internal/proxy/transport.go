package proxy

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	utls "github.com/refraction-networking/utls"
	"golang.org/x/net/http2"

	"notion-manager/internal/netutil"
)

var ErrInferenceIdleTimeout = fmt.Errorf("upstream inference idle timeout")

// Chrome TLS transport using uTLS to mimic Chrome's JA3/JA4 fingerprint.
// Uses http2.Transport for proper HTTP/2 support with custom TLS dial.
//
// dialChromeTLS reads AppConfig.Proxy.NotionProxy at dial time, so
// updating the global proxy via /admin/settings takes effect on the next
// connection without rebuilding the singleton. Idle pooled connections
// are torn down by RebuildChromeTransport so a flipped setting doesn't
// leak across the boundary.
var (
	chromeRoundTripperOnce sync.Once
	chromeRoundTripperH2   *http2.Transport
)

func getChromeRoundTripper() http.RoundTripper {
	chromeRoundTripperOnce.Do(func() {
		chromeRoundTripperH2 = &http2.Transport{
			DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
				return dialChromeTLS(ctx, network, addr)
			},
			DisableCompression: true, // We set Accept-Encoding ourselves
		}
	})
	return chromeRoundTripperH2
}

// RebuildChromeTransport drops every idle pooled connection so the next
// notion request re-dials and picks up the freshly-configured upstream
// proxy. Active in-flight requests are unaffected — the http2.Transport
// will simply not lend their connections to new callers anymore.
//
// Called from /admin/settings PUT after persisting a new proxy URL.
func RebuildChromeTransport() {
	getChromeRoundTripper() // ensure init
	if chromeRoundTripperH2 != nil {
		chromeRoundTripperH2.CloseIdleConnections()
	}
}

func dialChromeTLS(ctx context.Context, network, addr string) (net.Conn, error) {
	// Parse host for SNI
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = addr
	}

	// Honour the configured TLS dial timeout via a context deadline
	// before delegating to the shared proxy-aware dialer. The dialer
	// already enforces its own 30s connect timeout, but the project's
	// AppConfig.Timeouts.TLSDialTimeout governs the overall budget
	// (raw TCP + TLS handshake) so the caller's expectations hold
	// regardless of which path we take.
	dialCtx := ctx
	if to := AppConfig.TLSDialTimeoutDuration(); to > 0 {
		var cancel context.CancelFunc
		dialCtx, cancel = context.WithTimeout(ctx, to)
		defer cancel()
	}

	rawConn, err := netutil.DialThroughProxy(dialCtx, network, addr, AppConfig.NotionProxyURL())
	if err != nil {
		return nil, fmt.Errorf("tcp dial: %w", err)
	}

	// Create uTLS connection with Chrome fingerprint + ALPN h2
	tlsConfig := &utls.Config{
		ServerName:         host,
		InsecureSkipVerify: false,
		MinVersion:         tls.VersionTLS12,
		NextProtos:         []string{"h2", "http/1.1"},
	}

	tlsConn := utls.UClient(rawConn, tlsConfig, utls.HelloChrome_Auto)

	if err := tlsConn.HandshakeContext(dialCtx); err != nil {
		rawConn.Close()
		return nil, fmt.Errorf("tls handshake: %w", err)
	}

	return tlsConn, nil
}

// getChromeHTTPClient returns an http.Client with Chrome TLS fingerprint
func getChromeHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Transport: getChromeRoundTripper(),
		Timeout:   timeout,
	}
}

// doChromeRequestWithIdleTimeout limits periods with no upstream activity,
// not the total inference duration. Long responses remain valid as long as
// headers and body data keep arriving before each idle deadline.
func doChromeRequestWithIdleTimeout(req *http.Request, idleTimeout time.Duration) (*http.Response, error) {
	client := getChromeHTTPClient(0)
	if idleTimeout <= 0 {
		return client.Do(req)
	}

	ctx, cancel := context.WithCancel(req.Context())
	var headerTimedOut atomic.Bool
	timer := time.AfterFunc(idleTimeout, func() {
		headerTimedOut.Store(true)
		cancel()
	})
	resp, err := client.Do(req.Clone(ctx))
	timer.Stop()
	if err != nil {
		cancel()
		if headerTimedOut.Load() {
			return nil, fmt.Errorf("%w waiting for response headers after %s", ErrInferenceIdleTimeout, idleTimeout)
		}
		return nil, err
	}
	if headerTimedOut.Load() {
		resp.Body.Close()
		cancel()
		return nil, fmt.Errorf("%w waiting for response headers after %s", ErrInferenceIdleTimeout, idleTimeout)
	}
	resp.Body = &idleTimeoutReadCloser{
		body:    resp.Body,
		timeout: idleTimeout,
		cancel:  cancel,
	}
	return resp, nil
}

type idleTimeoutReadCloser struct {
	body      io.ReadCloser
	timeout   time.Duration
	cancel    context.CancelFunc
	closeOnce sync.Once
}

type idleReadResult struct {
	data []byte
	err  error
}

func (r *idleTimeoutReadCloser) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	resultCh := make(chan idleReadResult, 1)
	go func() {
		buf := make([]byte, len(p))
		n, err := r.body.Read(buf)
		resultCh <- idleReadResult{data: buf[:n], err: err}
	}()

	timer := time.NewTimer(r.timeout)
	defer timer.Stop()
	select {
	case result := <-resultCh:
		return copy(p, result.data), result.err
	case <-timer.C:
		_ = r.Close()
		return 0, fmt.Errorf("%w after %s without upstream data", ErrInferenceIdleTimeout, r.timeout)
	}
}

func (r *idleTimeoutReadCloser) Close() error {
	var closeErr error
	r.closeOnce.Do(func() {
		if r.cancel != nil {
			r.cancel()
		}
		closeErr = r.body.Close()
	})
	return closeErr
}
