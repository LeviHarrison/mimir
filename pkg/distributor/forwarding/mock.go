package forwarding

import (
	"context"
	"time"

	"github.com/grafana/mimir/pkg/mimirpb"
	"github.com/grafana/mimir/pkg/util/validation"
)

type mockForwarder struct {
	ingest bool

	// Optional callback to run in place of the actual forwarding request.
	forwardReqCallback func()
}

func NewMockForwarder(ingest bool, forwardReqCallback func()) Forwarder {
	return &mockForwarder{
		ingest:             ingest,
		forwardReqCallback: forwardReqCallback,
	}
}

func (m *mockForwarder) NewRequest(ctx context.Context, tenant string, _ validation.ForwardingRules) Request {
	return &mockForwardingRequest{forwarder: m}
}

type mockForwardingRequest struct {
	forwarder *mockForwarder
}

func (m *mockForwardingRequest) Add(sample mimirpb.PreallocTimeseries) bool {
	return m.forwarder.ingest
}

func (m *mockForwardingRequest) Send(ctx context.Context) *Promise {
	promise := NewPromise(time.Second, true)

	go func() {
		defer promise.done()

		if m.forwarder.forwardReqCallback != nil {
			m.forwarder.forwardReqCallback()
		}
	}()

	return promise
}
