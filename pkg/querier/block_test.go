// SPDX-License-Identifier: AGPL-3.0-only
// Provenance-includes-location: https://github.com/cortexproject/cortex/blob/master/pkg/querier/block_test.go
// Provenance-includes-license: Apache-2.0
// Provenance-includes-copyright: The Cortex Authors.

package querier

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"testing"
	"time"

	"github.com/prometheus/common/model"
	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/promql"
	"github.com/prometheus/prometheus/storage"
	"github.com/prometheus/prometheus/tsdb/chunkenc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/thanos-io/thanos/pkg/store/labelpb"
	"github.com/thanos-io/thanos/pkg/store/storepb"

	"github.com/grafana/mimir/pkg/util"
)

func TestBlockQuerierSeries(t *testing.T) {
	t.Parallel()

	// Init some test fixtures
	minTimestamp := time.Unix(1, 0)
	maxTimestamp := time.Unix(10, 0)

	tests := map[string]struct {
		series          *storepb.Series
		expectedMetric  labels.Labels
		expectedSamples []model.SamplePair
		expectedErr     string
	}{
		"empty series": {
			series:         &storepb.Series{},
			expectedMetric: labels.Labels(nil),
			expectedErr:    "no chunks",
		},
		"should return series on success": {
			series: &storepb.Series{
				Labels: []labelpb.ZLabel{
					{Name: "foo", Value: "bar"},
				},
				Chunks: []storepb.AggrChunk{
					{MinTime: minTimestamp.Unix() * 1000, MaxTime: maxTimestamp.Unix() * 1000, Raw: &storepb.Chunk{Type: storepb.Chunk_XOR, Data: mockTSDBChunkData()}},
				},
			},
			expectedMetric: labels.Labels{
				{Name: "foo", Value: "bar"},
			},
			expectedSamples: []model.SamplePair{
				{Timestamp: model.TimeFromUnixNano(time.Unix(1, 0).UnixNano()), Value: model.SampleValue(1)},
				{Timestamp: model.TimeFromUnixNano(time.Unix(2, 0).UnixNano()), Value: model.SampleValue(2)},
			},
		},
		"should return error on failure while reading encoded chunk data": {
			series: &storepb.Series{
				Labels: []labelpb.ZLabel{{Name: "foo", Value: "bar"}},
				Chunks: []storepb.AggrChunk{
					{MinTime: minTimestamp.Unix() * 1000, MaxTime: maxTimestamp.Unix() * 1000, Raw: &storepb.Chunk{Type: storepb.Chunk_XOR, Data: []byte{0, 1}}},
				},
			},
			expectedMetric: labels.Labels{labels.Label{Name: "foo", Value: "bar"}},
			expectedErr:    `cannot iterate chunk for series: {foo="bar"}: EOF`,
		},
	}

	for testName, testData := range tests {
		testData := testData

		t.Run(testName, func(t *testing.T) {
			series := newBlockQuerierSeries(labelpb.ZLabelsToPromLabels(testData.series.Labels), testData.series.Chunks)

			assert.Equal(t, testData.expectedMetric, series.Labels())

			sampleIx := 0

			it := series.Iterator()
			for it.Next() == chunkenc.ValFloat {
				ts, val := it.At()
				require.True(t, sampleIx < len(testData.expectedSamples))
				assert.Equal(t, int64(testData.expectedSamples[sampleIx].Timestamp), ts)
				assert.Equal(t, float64(testData.expectedSamples[sampleIx].Value), val)
				sampleIx++
			}
			// make sure we've got all expected samples
			require.Equal(t, sampleIx, len(testData.expectedSamples))

			if testData.expectedErr != "" {
				require.EqualError(t, it.Err(), testData.expectedErr)
			} else {
				require.NoError(t, it.Err())
			}
		})
	}
}

func mockTSDBChunkData() []byte {
	chunk := chunkenc.NewXORChunk()
	appender, err := chunk.Appender()
	if err != nil {
		panic(err)
	}

	appender.Append(time.Unix(1, 0).Unix()*1000, 1)
	appender.Append(time.Unix(2, 0).Unix()*1000, 2)

	return chunk.Bytes()
}

type timeRange struct {
	minT time.Time
	maxT time.Time
}

func TestBlockQuerierSeriesSet(t *testing.T) {
	now := time.Now()

	// It would be possible to split this test into smaller parts, but I prefer to keep
	// it as is, to also test transitions between series.

	getSeriesSet := func() *blockQuerierSeriesSet {
		return &blockQuerierSeriesSet{
			series: []*storepb.Series{
				// first, with one chunk.
				{
					Labels: mkZLabels("__name__", "first", "a", "a"),
					Chunks: []storepb.AggrChunk{
						createAggrChunkWithSineSamples(now, now.Add(100*time.Second-time.Millisecond), 3*time.Millisecond), // ceil(100 / 0.003) samples (= 33334)
					},
				},
				// continuation of previous series. Must have exact same labels.
				{
					Labels: mkZLabels("__name__", "first", "a", "a"),
					Chunks: []storepb.AggrChunk{
						createAggrChunkWithSineSamples(now.Add(100*time.Second), now.Add(200*time.Second-time.Millisecond), 3*time.Millisecond), // ceil(100 / 0.003) samples (= 33334) samples more, 66668 in total
					},
				},
				// second, with multiple chunks
				{
					Labels: mkZLabels("__name__", "second"),
					Chunks: []storepb.AggrChunk{
						// unordered chunks
						createAggrChunkWithSineSamples(now.Add(400*time.Second), now.Add(600*time.Second-5*time.Millisecond), 5*time.Millisecond), // 200 / 0.005 (= 40000 samples, = 120000 in total)
						createAggrChunkWithSineSamples(now.Add(200*time.Second), now.Add(400*time.Second-5*time.Millisecond), 5*time.Millisecond), // 200 / 0.005 (= 40000 samples)
						createAggrChunkWithSineSamples(now, now.Add(200*time.Second-5*time.Millisecond), 5*time.Millisecond),                      // 200 / 0.005 (= 40000 samples)
					},
				},
				// overlapping
				{
					Labels: mkZLabels("__name__", "overlapping"),
					Chunks: []storepb.AggrChunk{
						createAggrChunkWithSineSamples(now, now.Add(10*time.Second-5*time.Millisecond), 5*time.Millisecond), // 10 / 0.005 = 2000 samples
					},
				},
				{
					Labels: mkZLabels("__name__", "overlapping"),
					Chunks: []storepb.AggrChunk{
						// 10 / 0.005 = 2000 samples, but first 1000 are overlapping with previous series, so this chunk only contributes 1000
						createAggrChunkWithSineSamples(now.Add(5*time.Second), now.Add(15*time.Second-5*time.Millisecond), 5*time.Millisecond),
					},
				},
				// overlapping 2. Chunks here come in wrong order.
				{
					Labels: mkZLabels("__name__", "overlapping2"),
					Chunks: []storepb.AggrChunk{
						// entire range overlaps with the next chunk, so this chunks contributes 0 samples (it will be sorted as second)
						createAggrChunkWithSineSamples(now.Add(3*time.Second), now.Add(7*time.Second-5*time.Millisecond), 5*time.Millisecond),
					},
				},
				{
					Labels: mkZLabels("__name__", "overlapping2"),
					Chunks: []storepb.AggrChunk{
						// this chunk has completely overlaps previous chunk. Since its minTime is lower, it will be sorted as first.
						createAggrChunkWithSineSamples(now, now.Add(10*time.Second-5*time.Millisecond), 5*time.Millisecond), // 10 / 0.005 = 2000 samples
					},
				},
				{
					Labels: mkZLabels("__name__", "overlapping2"),
					Chunks: []storepb.AggrChunk{
						// no samples
						createAggrChunkWithSineSamples(now, now.Add(-5*time.Millisecond), 5*time.Millisecond),
					},
				},
				{
					Labels: mkZLabels("__name__", "overlapping2"),
					Chunks: []storepb.AggrChunk{
						// 2000 samples more (10 / 0.005)
						createAggrChunkWithSineSamples(now.Add(20*time.Second), now.Add(30*time.Second-5*time.Millisecond), 5*time.Millisecond),
					},
				},
				// many_empty_chunks is a series which contains many empty chunks and only a few that have data
				{
					Labels: mkZLabels("__name__", "many_empty_chunks"),
					Chunks: []storepb.AggrChunk{
						createAggrChunkWithSineSamples(now, now.Add(-5*time.Millisecond), 5*time.Millisecond),                                   // empty
						createAggrChunkWithSineSamples(now, now.Add(10*time.Second-5*time.Millisecond), 5*time.Millisecond),                     // 10 / 0.005 (= 2000 samples)
						createAggrChunkWithSineSamples(now.Add(10*time.Second), now.Add(10*time.Second-5*time.Millisecond), 5*time.Millisecond), // empty
						createAggrChunkWithSineSamples(now.Add(10*time.Second), now.Add(10*time.Second-5*time.Millisecond), 5*time.Millisecond), // empty
						createAggrChunkWithSineSamples(now.Add(10*time.Second), now.Add(20*time.Second-5*time.Millisecond), 5*time.Millisecond), // 10 / 0.005 (= 2000 samples, = 4000 in total)
						createAggrChunkWithSineSamples(now.Add(20*time.Second), now.Add(20*time.Second-5*time.Millisecond), 5*time.Millisecond), // empty
						createAggrChunkWithSineSamples(now.Add(20*time.Second), now.Add(20*time.Second-5*time.Millisecond), 5*time.Millisecond), // empty
						createAggrChunkWithSineSamples(now.Add(20*time.Second), now.Add(20*time.Second-5*time.Millisecond), 5*time.Millisecond), // empty
						createAggrChunkWithSineSamples(now.Add(20*time.Second), now.Add(30*time.Second-5*time.Millisecond), 5*time.Millisecond), // 10 / 0.005 (= 2000 samples, = 6000 in total)
						createAggrChunkWithSineSamples(now.Add(30*time.Second), now.Add(30*time.Second-5*time.Millisecond), 5*time.Millisecond), // empty
					},
				},
				// Two adjacent ranges with overlapping chunks in each range. Each overlapping chunk in a
				// range have +1 sample at +1ms timestamp compared to the previous one.
				{
					Labels: mkZLabels("__name__", "overlapping_chunks_with_additional_samples_in_sequence"),
					Chunks: []storepb.AggrChunk{
						// Range #1: [now, now+4ms]
						createAggrChunkWithSineSamples(now, now.Add(1*time.Millisecond), time.Millisecond),
						createAggrChunkWithSineSamples(now, now.Add(2*time.Millisecond), time.Millisecond),
						createAggrChunkWithSineSamples(now, now.Add(3*time.Millisecond), time.Millisecond),
						createAggrChunkWithSineSamples(now, now.Add(4*time.Millisecond), time.Millisecond),
						// Range #2: [now+5ms, now+7ms]
						createAggrChunkWithSineSamples(now.Add(5*time.Millisecond), now.Add(5*time.Millisecond), time.Millisecond),
						createAggrChunkWithSineSamples(now.Add(5*time.Millisecond), now.Add(6*time.Millisecond), time.Millisecond),
						createAggrChunkWithSineSamples(now.Add(5*time.Millisecond), now.Add(7*time.Millisecond), time.Millisecond),
					},
				},
			},
		}
	}

	// Test while calling .At() after varying numbers of samples have been consumed
	for _, callAtEvery := range []uint32{1, 3, 100, 971, 1000} {
		// Change scope of the variable to have tests working fine when running in parallel.
		callAtEvery := callAtEvery

		t.Run(fmt.Sprintf("consume with .Next() method, perform .At() after every %dth call to .Next()", callAtEvery), func(t *testing.T) {
			t.Parallel()

			advance := func(it chunkenc.Iterator, wantTs int64) bool { return it.Next() == chunkenc.ValFloat }
			ss := getSeriesSet()

			verifyNextSeries(t, ss, labels.FromStrings("__name__", "first", "a", "a"), 3*time.Millisecond, []timeRange{
				{now, now.Add(100*time.Second - time.Millisecond)},
				{now.Add(100 * time.Second), now.Add(200*time.Second - time.Millisecond)},
			}, 66668, callAtEvery, advance)
			verifyNextSeries(t, ss, labels.FromStrings("__name__", "second"), 5*time.Millisecond, []timeRange{
				{now, now.Add(600*time.Second - 5*time.Millisecond)},
			}, 120000, callAtEvery, advance)
			verifyNextSeries(t, ss, labels.FromStrings("__name__", "overlapping"), 5*time.Millisecond, []timeRange{
				{now, now.Add(15*time.Second - 5*time.Millisecond)},
			}, 3000, callAtEvery, advance)
			verifyNextSeries(t, ss, labels.FromStrings("__name__", "overlapping2"), 5*time.Millisecond, []timeRange{
				{now, now.Add(10*time.Second - 5*time.Millisecond)},
				{now.Add(20 * time.Second), now.Add(30*time.Second - 5*time.Millisecond)},
			}, 4000, callAtEvery, advance)
			verifyNextSeries(t, ss, labels.FromStrings("__name__", "many_empty_chunks"), 5*time.Millisecond, []timeRange{
				{now, now.Add(30*time.Second - 5*time.Millisecond)},
			}, 6000, callAtEvery, advance)
			verifyNextSeries(t, ss, labels.FromStrings("__name__", "overlapping_chunks_with_additional_samples_in_sequence"), time.Millisecond, []timeRange{
				{now, now.Add(7 * time.Millisecond)},
			}, 8, callAtEvery, advance)

			require.False(t, ss.Next())
		})

		t.Run(fmt.Sprintf("consume with .Seek() method, perform .At() after every %dth call to .Seek()", callAtEvery), func(t *testing.T) {
			t.Parallel()

			advance := func(it chunkenc.Iterator, wantTs int64) bool { return it.Seek(wantTs) == chunkenc.ValFloat }
			ss := getSeriesSet()

			verifyNextSeries(t, ss, labels.FromStrings("__name__", "first", "a", "a"), 3*time.Millisecond, []timeRange{
				{now, now.Add(100*time.Second - time.Millisecond)},
				{now.Add(100 * time.Second), now.Add(200*time.Second - time.Millisecond)},
			}, 66668, callAtEvery, advance)
			verifyNextSeries(t, ss, labels.FromStrings("__name__", "second"), 5*time.Millisecond, []timeRange{
				{now, now.Add(600*time.Second - 5*time.Millisecond)},
			}, 120000, callAtEvery, advance)
			verifyNextSeries(t, ss, labels.FromStrings("__name__", "overlapping"), 5*time.Millisecond, []timeRange{
				{now, now.Add(15*time.Second - 5*time.Millisecond)},
			}, 3000, callAtEvery, advance)
			verifyNextSeries(t, ss, labels.FromStrings("__name__", "overlapping2"), 5*time.Millisecond, []timeRange{
				{now, now.Add(10*time.Second - 5*time.Millisecond)},
				{now.Add(20 * time.Second), now.Add(30*time.Second - 5*time.Millisecond)},
			}, 4000, callAtEvery, advance)
			verifyNextSeries(t, ss, labels.FromStrings("__name__", "many_empty_chunks"), 5*time.Millisecond, []timeRange{
				{now, now.Add(30*time.Second - 5*time.Millisecond)},
			}, 6000, callAtEvery, advance)
			verifyNextSeries(t, ss, labels.FromStrings("__name__", "overlapping_chunks_with_additional_samples_in_sequence"), time.Millisecond, []timeRange{
				{now, now.Add(7 * time.Millisecond)},
			}, 8, callAtEvery, advance)

			require.False(t, ss.Next())
		})

		t.Run(fmt.Sprintf("consume with alternating calls to .Seek() and .Next() method, perform .At() after every %dth call to .Seek() or .Next()", callAtEvery), func(t *testing.T) {
			t.Parallel()

			var seek bool
			advance := func(it chunkenc.Iterator, wantTs int64) bool {
				seek = !seek
				if seek {
					return it.Seek(wantTs) == chunkenc.ValFloat
				}
				return it.Next() == chunkenc.ValFloat
			}
			ss := getSeriesSet()

			verifyNextSeries(t, ss, labels.FromStrings("__name__", "first", "a", "a"), 3*time.Millisecond, []timeRange{
				{now, now.Add(100*time.Second - time.Millisecond)},
				{now.Add(100 * time.Second), now.Add(200*time.Second - time.Millisecond)},
			}, 66668, callAtEvery, advance)
			verifyNextSeries(t, ss, labels.FromStrings("__name__", "second"), 5*time.Millisecond, []timeRange{
				{now, now.Add(600*time.Second - 5*time.Millisecond)},
			}, 120000, callAtEvery, advance)
			verifyNextSeries(t, ss, labels.FromStrings("__name__", "overlapping"), 5*time.Millisecond, []timeRange{
				{now, now.Add(15*time.Second - 5*time.Millisecond)},
			}, 3000, callAtEvery, advance)
			verifyNextSeries(t, ss, labels.FromStrings("__name__", "overlapping2"), 5*time.Millisecond, []timeRange{
				{now, now.Add(10*time.Second - 5*time.Millisecond)},
				{now.Add(20 * time.Second), now.Add(30*time.Second - 5*time.Millisecond)},
			}, 4000, callAtEvery, advance)
			verifyNextSeries(t, ss, labels.FromStrings("__name__", "many_empty_chunks"), 5*time.Millisecond, []timeRange{
				{now, now.Add(30*time.Second - 5*time.Millisecond)},
			}, 6000, callAtEvery, advance)
			verifyNextSeries(t, ss, labels.FromStrings("__name__", "overlapping_chunks_with_additional_samples_in_sequence"), time.Millisecond, []timeRange{
				{now, now.Add(7 * time.Millisecond)},
			}, 8, callAtEvery, advance)

			require.False(t, ss.Next())
		})
	}
}

// verifyNextSeries verifies a series by consuming it via a given consumer function.
// "step" is the time distance between samples.
// "ranges" is a slice of timeRanges where each timeRange consists of {minT,maxT}.
// "samples" is the expected total number of samples.
// "callAtEvery" defines after every how many samples we want to call .At().
// "advance" is a function which takes an iterator and advances its position.
func verifyNextSeries(t *testing.T, ss storage.SeriesSet, labels labels.Labels, step time.Duration, ranges []timeRange, samples, callAtEvery uint32, advance func(chunkenc.Iterator, int64) bool) {
	require.True(t, ss.Next())

	s := ss.At()
	require.Equal(t, labels, s.Labels())

	var count uint32
	it := s.Iterator()
	for _, r := range ranges {
		for wantTs := r.minT.UnixNano() / 1000000; wantTs <= r.maxT.UnixNano()/1000000; wantTs += step.Milliseconds() {
			require.True(t, advance(it, wantTs))

			if count%callAtEvery == 0 {
				gotTs, v := it.At()
				require.Equal(t, wantTs, gotTs)
				require.Equal(t, math.Sin(float64(wantTs)), v)
			}

			count++
		}
	}

	require.Equal(t, samples, count)
}

// createAggrChunkWithSineSamples takes a min/maxTime and a step duration, it generates a chunk given these specs.
// The minTime and maxTime are both inclusive.
func createAggrChunkWithSineSamples(minTime, maxTime time.Time, step time.Duration) storepb.AggrChunk {
	var samples []promql.Point

	minT := minTime.UnixNano() / 1000000
	maxT := maxTime.UnixNano() / 1000000
	stepMillis := step.Milliseconds()

	for t := minT; t <= maxT; t += stepMillis {
		samples = append(samples, promql.Point{T: t, V: math.Sin(float64(t))})
	}

	return createAggrChunk(minT, maxT, samples...)
}

func createAggrChunkWithSamples(samples ...promql.Point) storepb.AggrChunk {
	return createAggrChunk(samples[0].T, samples[len(samples)-1].T, samples...)
}

func createAggrChunk(minTime, maxTime int64, samples ...promql.Point) storepb.AggrChunk {
	// Ensure samples are sorted by timestamp.
	sort.Slice(samples, func(i, j int) bool {
		return samples[i].T < samples[j].T
	})

	chunk := chunkenc.NewXORChunk()
	appender, err := chunk.Appender()
	if err != nil {
		panic(err)
	}

	for _, s := range samples {
		appender.Append(s.T, s.V)
	}

	return storepb.AggrChunk{
		MinTime: minTime,
		MaxTime: maxTime,
		Raw: &storepb.Chunk{
			Type: storepb.Chunk_XOR,
			Data: chunk.Bytes(),
		},
	}
}

func mkZLabels(s ...string) []labelpb.ZLabel {
	var result []labelpb.ZLabel

	for i := 0; i+1 < len(s); i = i + 2 {
		result = append(result, labelpb.ZLabel{
			Name:  s[i],
			Value: s[i+1],
		})
	}

	return result
}

func mkLabels(s ...string) []labels.Label {
	return labelpb.ZLabelsToPromLabels(mkZLabels(s...))
}

func Benchmark_newBlockQuerierSeries(b *testing.B) {
	lbls := mkLabels(
		"__name__", "test",
		"label_1", "value_1",
		"label_2", "value_2",
		"label_3", "value_3",
		"label_4", "value_4",
		"label_5", "value_5",
		"label_6", "value_6",
		"label_7", "value_7",
		"label_8", "value_8",
		"label_9", "value_9")

	chunks := []storepb.AggrChunk{
		createAggrChunkWithSineSamples(time.Now(), time.Now().Add(-time.Hour), time.Minute),
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		newBlockQuerierSeries(lbls, chunks)
	}
}

func Benchmark_blockQuerierSeriesSet_iteration(b *testing.B) {
	const (
		numSeries          = 8000
		numSamplesPerChunk = 240
		numChunksPerSeries = 24
	)

	// Generate series.
	series := make([]*storepb.Series, 0, numSeries)
	for seriesID := 0; seriesID < numSeries; seriesID++ {
		lbls := mkZLabels("__name__", "test", "series_id", strconv.Itoa(seriesID))
		chunks := make([]storepb.AggrChunk, 0, numChunksPerSeries)

		// Create chunks with 1 sample per second.
		for minT := int64(0); minT < numChunksPerSeries*numSamplesPerChunk; minT += numSamplesPerChunk {
			chunks = append(chunks, createAggrChunkWithSineSamples(util.TimeFromMillis(minT), util.TimeFromMillis(minT+numSamplesPerChunk), time.Millisecond))
		}

		series = append(series, &storepb.Series{
			Labels: lbls,
			Chunks: chunks,
		})
	}

	b.ResetTimer()

	for n := 0; n < b.N; n++ {
		set := blockQuerierSeriesSet{series: series}

		for set.Next() {
			for t := set.At().Iterator(); t.Next() == chunkenc.ValFloat; {
				t.At()
			}
		}
	}
}

func Benchmark_blockQuerierSeriesSet_seek(b *testing.B) {
	const (
		numSeries          = 100
		numSamplesPerChunk = 120
		numChunksPerSeries = 500
		samplesPerStep     = 720 // equal to querying 15sec interval data with a "step" of 3h
	)

	// Generate series.
	series := make([]*storepb.Series, 0, numSeries)
	for seriesID := 0; seriesID < numSeries; seriesID++ {
		lbls := mkZLabels("__name__", "test", "series_id", strconv.Itoa(seriesID))
		chunks := make([]storepb.AggrChunk, 0, numChunksPerSeries)

		// Create chunks with 1 sample per second.
		for minT := int64(0); minT < numChunksPerSeries*numSamplesPerChunk; minT += numSamplesPerChunk {
			chunks = append(chunks, createAggrChunkWithSineSamples(util.TimeFromMillis(minT), util.TimeFromMillis(minT+numSamplesPerChunk), time.Millisecond))
		}

		series = append(series, &storepb.Series{
			Labels: lbls,
			Chunks: chunks,
		})
	}

	b.ResetTimer()

	for n := 0; n < b.N; n++ {
		set := blockQuerierSeriesSet{series: series}

		for set.Next() {
			seekT := int64(0)
			for t := set.At().Iterator(); t.Seek(seekT) == chunkenc.ValFloat; seekT += samplesPerStep {
				t.At()
			}
		}
	}
}
