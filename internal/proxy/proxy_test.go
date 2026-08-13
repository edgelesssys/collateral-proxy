// Copyright 2026 Edgeless Systems GmbH
// SPDX-License-Identifier: BUSL-1.1

package proxy

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/edgelesssys/collateral-proxy/internal/cache"
	"github.com/edgelesssys/collateral-proxy/internal/upstream"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// amdHWID is a sample hex-encoded 64-byte AMD hardware ID.
const amdHWID = "" +
	"a1b2c3d4e5f60718293a4b5c6d7e8f90" +
	"0102030405060708090a0b0c0d0e0f10" +
	"1112131415161718191a1b1c1d1e1f20" +
	"2122232425262728292a2b2c2d2e2f30"

func TestRoute(t *testing.T) {
	for _, c := range []struct {
		path     string
		host     string
		docType  string
		rejected bool
	}{
		{path: "/vcek/v1/Milan/" + amdHWID, host: "kdsintf.amd.com", docType: "ak-cert"},
		{path: "/vlek/v1/Milan/" + amdHWID, host: "kdsintf.amd.com", docType: "ak-cert"},
		{path: "/vcek/v1/Milan/crl", host: "kdsintf.amd.com", docType: "crl"},
		{path: "/vcek/v1/Milan/cert_chain", host: "kdsintf.amd.com", docType: "collateral"},
		{path: "/vcek/v1/Milan/not-a-hwid", host: "kdsintf.amd.com", docType: "unknown"},
		{path: "/vcek/v1/Milan/9af1a3beef", host: "kdsintf.amd.com", docType: "unknown"}, // too short to be a hardware ID

		{path: "/sgx/certification/v4/pckcert", host: "api.trustedservices.intel.com", docType: "ak-cert"},
		{path: "/sgx/certification/v4/pckcrl", host: "api.trustedservices.intel.com", docType: "crl"},
		{path: "/sgx/certification/v4/rootcacrl", host: "api.trustedservices.intel.com", docType: "crl"},
		{path: "/sgx/certification/v4/tcb", host: "api.trustedservices.intel.com", docType: "collateral"},
		{path: "/sgx/certification/v4/qe/identity", host: "api.trustedservices.intel.com", docType: "collateral"},
		{path: "/tdx/certification/v4/pckcert", host: "api.trustedservices.intel.com", docType: "ak-cert"},
		{path: "/sgx/certification/v4/something-new", host: "api.trustedservices.intel.com", docType: "unknown"},

		{path: "/IntelSGXRootCA.der", host: "certificates.trustedservices.intel.com", docType: "crl"},
		{path: "/IntelSGXsomethingelse", host: "certificates.trustedservices.intel.com", docType: "unknown"},

		{path: "/v1/rim/some-id", host: "rim.attestation.nvidia.com", docType: "collateral"},

		{path: "/", rejected: true},
		{path: "/evil/path", rejected: true},
	} {
		t.Run(c.path, func(t *testing.T) {
			host, docType, ok := route(c.path)
			if c.rejected {
				assert.False(t, ok)
				return
			}
			require.True(t, ok)
			assert.Equal(t, c.host, host)
			assert.Equal(t, c.docType, docType)
		})
	}
}

func TestReverseProxyCachesAndRoutes(t *testing.T) {
	var upstreamHits atomic.Int64
	upstreamSrv, fetchClient := newFakeKDS(t, func(w http.ResponseWriter, r *http.Request) {
		upstreamHits.Add(1)
		w.Header().Set("Cache-Control", "max-age=3600")
		w.Header().Set("X-Test-Header", "passthrough")
		_, _ = fmt.Fprintf(w, "vcek bytes for %s", r.URL.Path)
	})
	defer upstreamSrv.Close()

	cch, err := cache.New(t.TempDir())
	require.NoError(t, err)
	srv := New(slog.New(slog.DiscardHandler), cch, upstream.New(fetchClient), nil)

	proxySrv := httptest.NewServer(srv)
	defer proxySrv.Close()

	client := &http.Client{Timeout: 5 * time.Second}

	body, header := mustGet(t, client, proxySrv.URL+"/vcek/v1/Milan/abc")
	assert.Equal(t, "vcek bytes for /vcek/v1/Milan/abc", body)
	assert.Equal(t, "passthrough", header.Get("X-Test-Header"), "upstream response header not forwarded")
	assert.Equal(t, int64(1), upstreamHits.Load())

	body2, _ := mustGet(t, client, proxySrv.URL+"/vcek/v1/Milan/abc")
	assert.Equal(t, body, body2, "body mismatch on cache hit")
	assert.Equal(t, int64(1), upstreamHits.Load(), "second request should be served from cache")

	// NVIDIA RIM is routed to rim.attestation.nvidia.com and cached the same way.
	rimBody, _ := mustGet(t, client, proxySrv.URL+"/v1/rim/some-rim-id")
	assert.Equal(t, "vcek bytes for /v1/rim/some-rim-id", rimBody)
	assert.Equal(t, int64(2), upstreamHits.Load(), "RIM miss should hit upstream")

	_, _ = mustGet(t, client, proxySrv.URL+"/v1/rim/some-rim-id")
	assert.Equal(t, int64(2), upstreamHits.Load(), "RIM should be served from cache")
}

// TestUpstreamErrorIsNotCached covers the failure mode where a momentary vendor outage got
// stored and replayed as a cache hit for the whole TTL, long after the vendor recovered.
func TestUpstreamErrorIsNotCached(t *testing.T) {
	var upstreamHits atomic.Int64
	var failing atomic.Bool
	upstreamSrv, fetchClient := newFakeKDS(t, func(w http.ResponseWriter, r *http.Request) {
		upstreamHits.Add(1)
		if failing.Load() {
			http.Error(w, "upstream boom", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Cache-Control", "max-age=3600")
		_, _ = fmt.Fprintf(w, "cert chain for %s", r.URL.Path)
	})
	defer upstreamSrv.Close()

	cch, err := cache.New(t.TempDir())
	require.NoError(t, err)
	proxySrv := httptest.NewServer(New(slog.New(slog.DiscardHandler), cch, upstream.New(fetchClient), nil))
	defer proxySrv.Close()

	client := &http.Client{Timeout: 5 * time.Second}
	const path = "/vcek/v1/Milan/cert_chain"

	failing.Store(true)
	status, body := get(t, client, proxySrv.URL+path)
	assert.Equal(t, http.StatusInternalServerError, status)
	assert.Contains(t, body, "upstream boom")
	assert.Equal(t, int64(1), upstreamHits.Load())

	// The error must not have been cached: the next request goes upstream again.
	status, _ = get(t, client, proxySrv.URL+path)
	assert.Equal(t, http.StatusInternalServerError, status)
	assert.Equal(t, int64(2), upstreamHits.Load(), "error response was served from cache")

	// Once upstream recovers, the proxy serves and caches the real collateral.
	failing.Store(false)
	status, body = get(t, client, proxySrv.URL+path)
	assert.Equal(t, http.StatusOK, status)
	assert.Equal(t, "cert chain for "+path, body)
	assert.Equal(t, int64(3), upstreamHits.Load())

	status, body = get(t, client, proxySrv.URL+path)
	assert.Equal(t, http.StatusOK, status)
	assert.Equal(t, "cert chain for "+path, body)
	assert.Equal(t, int64(3), upstreamHits.Load(), "success should be served from cache")
}

func TestStaleServedOnUpstreamStatus(t *testing.T) {
	var upstreamStatus atomic.Int64
	upstreamSrv, fetchClient := newFakeKDS(t, func(w http.ResponseWriter, r *http.Request) {
		if code := int(upstreamStatus.Load()); code != http.StatusOK {
			http.Error(w, "upstream says no", code)
			return
		}
		w.Header().Set("Cache-Control", "max-age=1")
		_, _ = fmt.Fprintf(w, "cert chain for %s", r.URL.Path)
	})
	defer upstreamSrv.Close()

	cch, err := cache.New(t.TempDir())
	require.NoError(t, err)
	proxySrv := httptest.NewServer(New(slog.New(slog.DiscardHandler), cch, upstream.New(fetchClient), nil))
	defer proxySrv.Close()

	client := &http.Client{Timeout: 5 * time.Second}
	const path = "/vcek/v1/Milan/cert_chain"

	// Warm the cache, then let the entry go stale.
	upstreamStatus.Store(http.StatusOK)
	status, want := get(t, client, proxySrv.URL+path)
	require.Equal(t, http.StatusOK, status)
	entry, _ := cch.Get("https://kdsintf.amd.com" + path)
	require.NotNil(t, entry)
	entry.FreshUntil = time.Now().Add(-time.Hour)

	// A transient upstream failure is covered by the stale entry.
	for _, code := range []int{http.StatusInternalServerError, http.StatusTooManyRequests} {
		upstreamStatus.Store(int64(code))
		status, body := get(t, client, proxySrv.URL+path)
		assert.Equal(t, http.StatusOK, status, "upstream %d should be covered by the stale entry", code)
		assert.Equal(t, want, body)
	}

	// A definitive answer is relayed as-is, even with a stale entry around.
	upstreamStatus.Store(http.StatusNotFound)
	status, _ = get(t, client, proxySrv.URL+path)
	assert.Equal(t, http.StatusNotFound, status)
}

func TestRejectsUnknownPath(t *testing.T) {
	cch, err := cache.New(t.TempDir())
	require.NoError(t, err)
	srv := New(slog.New(slog.DiscardHandler), cch, upstream.New(&http.Client{Timeout: time.Second}), nil)
	proxySrv := httptest.NewServer(srv)
	defer proxySrv.Close()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, proxySrv.URL+"/evil/path", nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusNotFound, resp.StatusCode)
}

// newFakeKDS starts a TLS server running handler and returns it along with an HTTP client
// that resolves the vendor endpoints to it.
func newFakeKDS(t *testing.T, handler http.HandlerFunc) (*httptest.Server, *http.Client) {
	t.Helper()
	srv := httptest.NewTLSServer(handler)
	srvURL, err := url.Parse(srv.URL)
	require.NoError(t, err)

	dialer := &net.Dialer{}
	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				if strings.HasPrefix(addr, "kdsintf.amd.com:") ||
					strings.HasPrefix(addr, "rim.attestation.nvidia.com:") {
					return dialer.DialContext(ctx, network, srvURL.Host)
				}
				return dialer.DialContext(ctx, network, addr)
			},
		},
		Timeout: 5 * time.Second,
	}
	return srv, client
}

// get performs a GET and returns the response status and body.
func get(t *testing.T, c *http.Client, url string) (int, string) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	require.NoError(t, err, "build request %s", url)
	resp, err := c.Do(req)
	require.NoError(t, err, "GET %s", url)
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err, "read body")
	return resp.StatusCode, string(b)
}

func mustGet(t *testing.T, c *http.Client, url string) (string, http.Header) {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	require.NoError(t, err, "build request %s", url)
	resp, err := c.Do(req)
	require.NoError(t, err, "GET %s", url)
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	require.NoError(t, err, "read body")
	require.Equal(t, http.StatusOK, resp.StatusCode, "body=%s", b)
	return string(b), resp.Header
}
