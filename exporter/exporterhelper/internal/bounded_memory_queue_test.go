// Copyright The OpenTelemetry Authors
// Copyright (c) 2019 The Jaeger Authors.
// Copyright (c) 2017 Uber Technologies, Inc.
// SPDX-License-Identifier: Apache-2.0

package internal

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.opentelemetry.io/collector/component/componenttest"
)

// In this test we run a queue with capacity 1 and a single consumer.
// We want to test the overflow behavior, so we block the consumer
// by holding a startLock before submitting items to the queue.
func TestBoundedQueue(t *testing.T) {
	q := NewBoundedMemoryQueue[string](MemoryQueueSettings[string]{Sizer: &RequestSizer[string]{}, Capacity: 1})

	assert.NoError(t, q.Offer(context.Background(), "a"))

	numConsumed := 0
	assert.True(t, q.Consume(func(ctx context.Context, item string) error {
		assert.Equal(t, "a", item)
		numConsumed++
		return nil
	}))
	assert.Equal(t, 1, numConsumed)
	assert.Equal(t, 0, q.Size())

	// produce two more items. The first one should be accepted, but not consumed.
	assert.NoError(t, q.Offer(context.Background(), "b"))
	assert.Equal(t, 1, q.Size())

	// the second should be rejected since the queue is full
	assert.ErrorIs(t, q.Offer(context.Background(), "c"), ErrQueueIsFull)
	assert.Equal(t, 1, q.Size())

	assert.True(t, q.Consume(func(ctx context.Context, item string) error {
		assert.Equal(t, "b", item)
		numConsumed++
		return nil
	}))
	assert.Equal(t, 2, numConsumed)

	for _, toAddItem := range []string{"d", "e", "f"} {
		assert.NoError(t, q.Offer(context.Background(), toAddItem))
		assert.True(t, q.Consume(func(ctx context.Context, item string) error {
			assert.Equal(t, toAddItem, item)
			numConsumed++
			return nil
		}))
	}
	assert.Equal(t, 5, numConsumed)
	assert.NoError(t, q.Shutdown(context.Background()))
	assert.False(t, q.Consume(func(ctx context.Context, item string) error {
		panic(item)
	}))
}

// In this test we run a queue with many items and a slow consumer.
// When the queue is stopped, the remaining items should be processed.
// Due to the way q.Stop() waits for all consumers to finish, the
// same lock strategy use above will not work, as calling Unlock
// only after Stop will mean the consumers are still locked while
// trying to perform the final consumptions.
func TestShutdownWhileNotEmpty(t *testing.T) {
	q := NewBoundedMemoryQueue[string](MemoryQueueSettings[string]{Sizer: &RequestSizer[string]{}, Capacity: 1000})

	assert.NoError(t, q.Start(context.Background(), componenttest.NewNopHost()))
	for i := 0; i < 10; i++ {
		assert.NoError(t, q.Offer(context.Background(), strconv.FormatInt(int64(i), 10)))
	}
	assert.NoError(t, q.Shutdown(context.Background()))

	assert.Equal(t, 10, q.Size())
	numConsumed := 0
	for i := 0; i < 10; i++ {
		assert.True(t, q.Consume(func(ctx context.Context, item string) error {
			assert.Equal(t, strconv.FormatInt(int64(i), 10), item)
			numConsumed++
			return nil
		}))
	}
	assert.Equal(t, 10, numConsumed)
	assert.Equal(t, 0, q.Size())

	assert.False(t, q.Consume(func(ctx context.Context, item string) error {
		panic(item)
	}))
}

func Benchmark_QueueUsage_10000_requests_1_50000(b *testing.B) {
	benchmarkQueueUsage(b, &RequestSizer[fakeReq]{}, 10000, 1, 50000)
}

func Benchmark_QueueUsage_10000_requests_10_50000(b *testing.B) {
	benchmarkQueueUsage(b, &RequestSizer[fakeReq]{}, 10000, 10, 50000)
}

func Benchmark_QueueUsage_50000_requests_1_50000(b *testing.B) {
	benchmarkQueueUsage(b, &RequestSizer[fakeReq]{}, 50000, 1, 50000)
}

func Benchmark_QueueUsage_50000_requests_10_50000(b *testing.B) {
	benchmarkQueueUsage(b, &RequestSizer[fakeReq]{}, 50000, 10, 50000)
}

func Benchmark_QueueUsage_10000_requests_1_250000(b *testing.B) {
	benchmarkQueueUsage(b, &RequestSizer[fakeReq]{}, 10000, 1, 250000)
}

func Benchmark_QueueUsage_10000_requests_10_250000(b *testing.B) {
	benchmarkQueueUsage(b, &RequestSizer[fakeReq]{}, 10000, 10, 250000)
}

func Benchmark_QueueUsage_1M_items_10_250k(b *testing.B) {
	benchmarkQueueUsage(b, &ItemsSizer[fakeReq]{}, 1000000, 10, 250000)
}

func Benchmark_QueueUsage_1M_items_10_1M(b *testing.B) {
	benchmarkQueueUsage(b, &ItemsSizer[fakeReq]{}, 1000000, 10, 1000000)
}

func Benchmark_QueueUsage_100M_items_10_10M(b *testing.B) {
	benchmarkQueueUsage(b, &ItemsSizer[fakeReq]{}, 100000000, 10, 10000000)
}

func TestQueueUsage(t *testing.T) {
	t.Run("with enough workers", func(t *testing.T) {
		queueUsage(t, &RequestSizer[fakeReq]{}, 10000, 5, 1000)
	})
	t.Run("past capacity", func(t *testing.T) {
		queueUsage(t, &RequestSizer[fakeReq]{}, 10000, 2, 50000)
	})
}

func benchmarkQueueUsage(b *testing.B, sizer Sizer[fakeReq], capacity int, numConsumers int,
	numberOfItems int) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		queueUsage(b, sizer, capacity, numConsumers, numberOfItems)
	}
}

func queueUsage(tb testing.TB, sizer Sizer[fakeReq], capacity int, numConsumers int, numberOfItems int) {
	var wg sync.WaitGroup
	wg.Add(numberOfItems)
	q := NewBoundedMemoryQueue[fakeReq](MemoryQueueSettings[fakeReq]{Sizer: sizer, Capacity: capacity})
	consumers := NewQueueConsumers(q, numConsumers, func(context.Context, fakeReq) error {
		wg.Done()
		return nil
	})
	require.NoError(tb, consumers.Start(context.Background(), componenttest.NewNopHost()))
	for j := 0; j < numberOfItems; j++ {
		if err := q.Offer(context.Background(), fakeReq{10}); errors.Is(err, ErrQueueIsFull) {
			wg.Done()
		}
	}
	assert.NoError(tb, consumers.Shutdown(context.Background()))

	wg.Wait()
}

func TestZeroSizeNoConsumers(t *testing.T) {
	q := NewBoundedMemoryQueue[string](MemoryQueueSettings[string]{Sizer: &RequestSizer[string]{}, Capacity: 0})

	err := q.Start(context.Background(), componenttest.NewNopHost())
	assert.NoError(t, err)

	assert.ErrorIs(t, q.Offer(context.Background(), "a"), ErrQueueIsFull) // in process

	assert.NoError(t, q.Shutdown(context.Background()))
}
