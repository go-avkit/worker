// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package serve

import (
	"bytes"
	"image/jpeg"
	"math"
	"testing"

	"github.com/go-avkit/worker/proto"
)

// FuzzHandle feeds Handle hostile requests (any operation, key, time and
// limits) with hostile payloads (seeded from real files). Whatever comes in:
// no panic, an answer that carries the request's ID, a refusal that has a
// known code and a message, a successful poster that is a JPEG of the size it
// says, a size within the worker's own bounds. The corpus in testdata/fuzz runs
// as ordinary tests.
func FuzzHandle(f *testing.F) {
	for _, n := range []string{"bframes.mp4", "bframes.mkv", "hvc1.mp4"} {
		d := read(f, n)
		f.Add(d, "poster", 0.5, true, 0, 0, 0, 0)
		f.Add(d, "probe", 0.0, true, 1, 3, 1, 8)
	}
	f.Add([]byte(nil), "poster", -1.0, false, 0, 0, 0, 0)
	f.Add(ftyp, "poster", 1e300, true, math.MaxInt, math.MaxInt, math.MaxInt, math.MaxInt)
	f.Add([]byte("hello"), "\xff\x00", 0.0, true, -5, -5, -5, -5)
	known := map[string]bool{proto.CodeLimit: true, proto.CodeSyntax: true, proto.CodeCodec: true, proto.CodeNotHeld: true}
	s := &Server{}
	f.Fuzz(func(t *testing.T, data []byte, op string, tm float64, hasData bool, maxBytes, maxBoxes, maxDepth, posterW int) {
		if len(data) > 1<<20 {
			return
		}
		req := proto.Request{
			ID: 7, Op: op, Key: proto.Fingerprint(data), HasData: hasData, T: tm,
			Limits: proto.Limits{MaxBytes: maxBytes, MaxBoxes: maxBoxes, MaxDepth: maxDepth, PosterWidth: posterW},
		}
		res, img := s.Handle(req, data)
		if res.ID != 7 {
			t.Fatalf("answer to id %d", res.ID)
		}
		if !res.OK() {
			if !known[res.Code] || res.Error == "" || img != nil {
				t.Fatalf("refusal %+v with %d image bytes", res, len(img))
			}
			return
		}
		if op == proto.OpPoster {
			pic, err := jpeg.Decode(bytes.NewReader(img))
			if err != nil || pic.Bounds().Dx() != res.Width || pic.Bounds().Dy() != res.Height {
				t.Fatalf("the poster is not the JPEG it says: %v", err)
			}
			if res.Width > proto.DefaultLimits.PosterWidth || res.Width > proto.Clamp(req.Limits).PosterWidth {
				t.Fatalf("a poster %d wide, over the clamped width %d", res.Width, proto.Clamp(req.Limits).PosterWidth)
			}
		}
		if res.Width > proto.DefaultLimits.MaxWidth || res.Height > proto.DefaultLimits.MaxHeight {
			t.Fatalf("a frame %dx%d over the worker's bounds", res.Width, res.Height)
		}
	})
}
