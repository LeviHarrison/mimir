// SPDX-License-Identifier: AGPL-3.0-only

package forwarding

import (
	"net/http"
	"sync"
	"time"

	"github.com/pkg/errors"
	"github.com/weaveworks/common/httpgrpc"
)

var promiseTimeout = errors.New("timed out while waiting for forwarding promise")

// Promise is used to asynchronously communicate the status and result of a forwarding request.
type Promise struct {
	timeout         time.Duration
	propagateErrors bool
	doneCh          chan struct{}
	errMtx          sync.Mutex
	err             error
}

func NewPromise(timeout time.Duration, propagateErrors bool) *Promise {
	return &Promise{
		propagateErrors: propagateErrors,
		timeout:         timeout,
		doneCh:          make(chan struct{}),
	}
}

func (s *Promise) Wait() {
	// awaitTimeout limits for how long AwaitDone can block.
	awaitTimeout := time.NewTimer(s.timeout)

	select {
	case <-s.doneCh:
		awaitTimeout.Stop()
	case <-awaitTimeout.C:
		s.errMtx.Lock()
		defer s.errMtx.Unlock()

		s.err = promiseTimeout
	}
}

// Error waits until the promise is done and then potentially returns an error or nil.
func (s *Promise) Error() error {
	s.Wait()

	s.errMtx.Lock()
	defer s.errMtx.Unlock()

	return s.err
}

func (s *Promise) ErrorAsHTTPGrpc() error {
	err := s.Error()

	if err == nil {
		return err
	}

	if errors.As(err, &recoverableError{}) {
		return httpgrpc.Errorf(http.StatusInternalServerError, err.Error())
	}

	return httpgrpc.Errorf(http.StatusBadRequest, err.Error())
}

// done marks the promise as done.
func (s *Promise) done() {
	close(s.doneCh)
}

// setError sets an error as the result of the promise.
// Recoverable errors overwrite non-recoverable errors, otherwise the first error is kept.
func (s *Promise) setError(err error) {
	if err == nil || !s.propagateErrors {
		return
	}

	s.errMtx.Lock()
	defer s.errMtx.Unlock()

	if s.err == nil {
		s.err = err
		return
	}

	if !errors.As(s.err, &recoverableError{}) && errors.As(err, &recoverableError{}) {
		// If the current error "s.err" is not recoverable and the newly set error "err" is recoverable then we want
		// to replace s.err with the newly set error because recoverable errors should take precedence.
		s.err = err
	}

}
