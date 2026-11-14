// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package proto

import (
	"math"
	"testing"
)

// FuzzDecodeRequest: no header, however hostile, panics the decoder; a header
// that decodes holds a bounded key; and the limits the worker would apply for
// it are, field by field, in 1..DefaultLimits. The corpus in testdata/fuzz runs
// as ordinary tests.
func FuzzDecodeRequest(f *testing.F) {
	for _, s := range []string{
		`{"id":1,"op":"poster","key":"9-ab","hasData":true,"format":"mp4","t":1.5,"limits":{"MaxBytes":67108864,"MaxBoxes":262144,"MaxDepth":16,"MaxWidth":4096,"MaxHeight":4096,"MaxSamples":600,"MaxTrackSamples":262144,"MaxSampleBytes":8388608,"PosterWidth":1280}}`,
		`{"id":2,"op":"probe","key":"0-0","limits":{"MaxBytes":9223372036854775807,"MaxBoxes":-1,"MaxDepth":0,"MaxWidth":99999999,"PosterWidth":-9223372036854775808}}`,
		`{"id":3,"limits":{"MaxBytes":1e30}}`, `{"id":4} {"id":5}`, `[]`, `null`, `{"limits":null}`, `{"t":1e999}`, ``,
	} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		req, err := DecodeRequest(b)
		if err != nil {
			if _, ok := err.(*Error); !ok {
				t.Fatalf("an error with no code: %v", err)
			}
			return
		}
		if len(req.Key) > MaxKeyBytes {
			t.Fatalf("a key of %d bytes", len(req.Key))
		}
		within(t, req.Limits, Clamp(req.Limits))
	})
}

// FuzzClampOnlyLowers: for every ask, every effective bound is at most the
// worker's default and positive, and a lowering is kept exactly.
func FuzzClampOnlyLowers(f *testing.F) {
	f.Add(0, 0, 0, 0, 0, 0, 0, 0, 0)
	f.Add(math.MaxInt, math.MaxInt, math.MaxInt, math.MaxInt, math.MaxInt, math.MaxInt, math.MaxInt, math.MaxInt, math.MaxInt)
	f.Add(-1, math.MinInt, 5, 1, 4097, 4096, 600, 601, 1280)
	f.Fuzz(func(t *testing.T, a, b, c, d, e, g, h, i, j int) {
		ask := Limits{MaxBytes: a, MaxBoxes: b, MaxDepth: c, MaxWidth: d, MaxHeight: e, MaxSamples: g, MaxTrackSamples: h, MaxSampleBytes: i, PosterWidth: j}
		within(t, ask, Clamp(ask))
	})
}
