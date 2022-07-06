// SPDX-License-Identifier: AGPL-3.0-only

package forwarding

import (
	"net/http"
	"testing"
	"time"

	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
	"github.com/weaveworks/common/httpgrpc"
)

func TestWaitingForPromiseDone(t *testing.T) {
	promise := NewPromise(time.Second, true)

	startingToWait := make(chan struct{})
	doneWaiting := make(chan struct{})

	go func() {
		close(startingToWait)
		promise.Wait()
		close(doneWaiting)
	}()

	<-startingToWait

	// Let the promise wait for a bit.
	time.Sleep(100 * time.Millisecond)

	// Check if promise is still waiting.
	select {
	case <-doneWaiting:
		t.Fatal("Expected promise to still be waiting")
	default:
	}

	// Mark the promise as done, the go routine should now close the doneWaiting channel.
	promise.done()

	// Give the go routine some time to finish.
	time.Sleep(100 * time.Millisecond)

	// Check if doneWaiting channel is closed.
	select {
	case <-doneWaiting:
	default:
		t.Fatal("Expected promise to be done waiting")
	}

	require.Nil(t, promise.Error())
}

func TestPromiseErrorPropagation(t *testing.T) {
	testErr := errors.New("Test error")
	type testCase struct {
		name        string
		propagation bool
		expectErr   error
	}

	testCases := []testCase{
		{
			name:        "Expect error to be propagated with propagation enabled",
			propagation: true,
			expectErr:   testErr,
		}, {
			name:        "Expect error to not be propagated with propagation disabled",
			propagation: false,
			expectErr:   nil,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			promise := NewPromise(time.Second, tc.propagation)
			promise.setError(testErr)
			promise.done()
			gotErr := promise.Error()
			require.Equal(t, tc.expectErr, gotErr)
		})
	}
}

func TestPromiseTimeout(t *testing.T) {
	timeout := 100 * time.Millisecond
	promise := NewPromise(timeout, false)

	now := time.Now()
	promise.Wait()
	elapsed := time.Since(now)
	require.Greater(t, elapsed, timeout)

	err := promise.Error()
	require.Equal(t, promiseTimeout, err)
}

func TestPromiseErrAsHTTPGrpc(t *testing.T) {
	testErr := errors.New("Test error")
	type testCase struct {
		name      string
		setErr    error
		expectErr error
	}

	testCases := []testCase{
		{
			name:      "Expect recoverable error to result in internal server error",
			setErr:    recoverableError{testErr},
			expectErr: httpgrpc.Errorf(http.StatusInternalServerError, testErr.Error()),
		}, {
			name:      "Expect non-recoverable error to result in bad request error",
			setErr:    testErr,
			expectErr: httpgrpc.Errorf(http.StatusBadRequest, testErr.Error()),
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			promise := NewPromise(time.Second, true)
			promise.done()

			promise.setError(tc.setErr)
			gotErr := promise.ErrorAsHTTPGrpc()
			require.Equal(t, tc.expectErr, gotErr)
		})
	}
}
