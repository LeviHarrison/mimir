// SPDX-License-Identifier: AGPL-3.0-only
// Provenance-includes-location: https://github.com/cortexproject/cortex/blob/master/pkg/chunk/cache/mock.go
// Provenance-includes-license: Apache-2.0
// Provenance-includes-copyright: The Cortex Authors.

package cache

import (
	"context"
	"sync"
	"time"

	"go.uber.org/atomic"
)

type MockCache struct {
	mu    sync.Mutex
	cache map[string]cacheItem
}

func NewMockCache() *MockCache {
	c := &MockCache{}
	c.Flush()
	return c
}

func (m *MockCache) Store(_ context.Context, data map[string][]byte, ttl time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()

	exp := time.Now().Add(ttl)
	for key, val := range data {
		m.cache[key] = cacheItem{data: val, expiresAt: exp}
	}
}

func (m *MockCache) Fetch(_ context.Context, keys []string) map[string][]byte {
	m.mu.Lock()
	defer m.mu.Unlock()

	found := make(map[string][]byte, len(keys))

	now := time.Now()
	for _, k := range keys {
		v, ok := m.cache[k]
		if ok && now.Before(v.expiresAt) {
			found[k] = v.data
		}
	}

	return found
}

func (m *MockCache) Name() string {
	return "mock"
}

func (m *MockCache) PutValue(_ []byte) {}

func (m *MockCache) Flush() {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.cache = map[string]cacheItem{}
}

func (m *MockCache) Delete(key string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	delete(m.cache, key)
}

// InstrumentedMockCache is a mocked cache implementation which also tracks the number
// of times its functions are called.
type InstrumentedMockCache struct {
	cache      *MockCache
	storeCount atomic.Int32
	fetchCount atomic.Int32
}

// NewInstrumentedMockCache makes a new InstrumentedMockCache.
func NewInstrumentedMockCache() *InstrumentedMockCache {
	return &InstrumentedMockCache{
		cache: NewMockCache(),
	}
}

func (m *InstrumentedMockCache) Store(ctx context.Context, data map[string][]byte, ttl time.Duration) {
	m.storeCount.Inc()
	m.cache.Store(ctx, data, ttl)
}

func (m *InstrumentedMockCache) Fetch(ctx context.Context, keys []string) map[string][]byte {
	m.fetchCount.Inc()
	return m.cache.Fetch(ctx, keys)
}

func (m *InstrumentedMockCache) Name() string {
	return m.cache.Name()
}

func (m *InstrumentedMockCache) PutValue(b []byte) {
	m.cache.PutValue(b)
}

func (m *InstrumentedMockCache) CountStoreCalls() int {
	return int(m.storeCount.Load())
}

func (m *InstrumentedMockCache) CountFetchCalls() int {
	return int(m.fetchCount.Load())
}
