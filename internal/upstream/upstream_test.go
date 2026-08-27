// Copyright 2026 Edgeless Systems GmbH
// SPDX-License-Identifier: BUSL-1.1

package upstream

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestCallerCancellationDoesNotAbortFetch covers the case of a client disconnecting while the upstream request it triggered is still in flight.
func TestCallerCancellationDoesNotAbortFetch(t *testing.T) {
	release := make(chan struct{})
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		<-release
		select {
		case <-r.Context().Done():
			// The server saw the fetch get canceled; fail via an empty body below.
			return
		default:
		}
		_, _ = w.Write([]byte("collateral"))
	}))
	defer srv.Close()

	f := New(srv.Client())

	canceledCtx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Go(func() {
		res, err := f.Get(canceledCtx, srv.URL)
		require.NoError(t, err)
		assert.Equal(t, "collateral", string(res.Body))
	})

	cancel()
	close(release)
	wg.Wait()
	assert.Equal(t, 1, hits)
}
