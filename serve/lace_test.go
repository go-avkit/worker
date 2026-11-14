// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package serve

import (
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/go-avkit/worker/proto"
)

var (
	ebmlHead = []byte{0x1A, 0x45, 0xDF, 0xA3, 0x80}
	unknown  = []byte{0x01, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF}
)

// block is a SimpleBlock of `lacing` (0x02 Xiph, 0x04 fixed, 0x06 EBML) whose
// count byte says n frames; the 7 bytes of the element stand for n frames to a
// reader that unlaces up front (go-avkit/avkit#120). The frame bytes are not
// there: the header is what the pre-scan reads.
func block(id byte, track byte, lacing byte, n int) []byte {
	body := []byte{0x80 | track, 0, 0, lacing}
	if lacing != 0 {
		body = append(body, byte(n-1))
	}
	return append([]byte{id, 0x80 | byte(len(body))}, body...)
}

// laced is a Matroska file of blocks laced n frames each, cycling over tracks.
func laced(blocks, n, tracks int, group bool) []byte {
	d := append(append([]byte{}, ebmlHead...), 0x18, 0x53, 0x80, 0x67)
	d = append(append(d, unknown...), 0x1F, 0x43, 0xB6, 0x75)
	d = append(d, unknown...)
	for i := range blocks {
		b := block(idSimpleBlock, byte(1+i%tracks), 0x04, n)
		if group { // a BlockGroup (0xA0) around a Block (0xA1)
			in := block(idBlock, byte(1+i%tracks), 0x04, n)
			b = append([]byte{0xA0, 0x80 | byte(len(in))}, in...)
		}
		d = append(d, b...)
	}
	return d
}

// The reader's own bound (avkit v0.17.0) is a second line: with the pre-scan
// bypassed (open called directly), the reader still refuses the bomb as
// video.limit, and a lowered MaxTrackSamples is passed on to it.
func TestReaderRefusesLacedBombWithoutThePrescan(t *testing.T) {
	data := laced(1000, 256, 1, false) // 256,000 frames
	lim := proto.DefaultLimits
	lim.MaxTrackSamples = 100000
	if _, _, err := sniff(data, lim); err == nil {
		t.Fatal("the pre-scan accepted it: this test would not reach the reader")
	}
	_, err := open(data, "webm", "video/webm", lim)
	var e *proto.Error
	if !errors.As(err, &e) || e.Code != proto.CodeLimit || !strings.Contains(e.Msg, "reader limit") {
		t.Errorf("open with the pre-scan bypassed: %v", err)
	}
}

func TestBlockFrames(t *testing.T) {
	for name, tc := range map[string]struct {
		body   []byte
		track  uint64
		frames int
		ok     bool
	}{
		"unlaced":         {[]byte{0x81, 0, 0, 0x80, 'x'}, 1, 1, true},
		"fixed 256":       {[]byte{0x81, 0, 0, 0x04, 0xFF}, 1, 256, true},
		"xiph 3":          {[]byte{0x82, 0, 0, 0x02, 2}, 2, 3, true},
		"ebml 2":          {[]byte{0x83, 0, 0, 0x06, 1}, 3, 2, true},
		"track on 2 byte": {[]byte{0x40, 0x05, 0, 0, 0x04, 7}, 5, 8, true},
		"short":           {[]byte{0x81, 0, 0}, 0, 0, false},
		"laced no count":  {[]byte{0x81, 0, 0, 0x04}, 0, 0, false},
		"empty":           {nil, 0, 0, false},
		"bad track":       {[]byte{0x00, 0, 0, 0}, 0, 0, false},
	} {
		track, frames, ok := blockFrames(tc.body)
		if ok != tc.ok || frames != tc.frames || (ok && track != tc.track) {
			t.Errorf("%s: track %d, %d frames, %v", name, track, frames, ok)
		}
	}
}

// The pre-scan bounds the frames, laced ones included: a track may list
// MaxTrackSamples (262144) and the file twice that. 1023 blocks of 256 frames
// are 261,888; 1025 are 262,400.
func TestLacedFramesAreCountedAtTheBound(t *testing.T) {
	lim := proto.DefaultLimits
	if _, _, err := sniff(laced(1023, 256, 1, false), lim); err != nil {
		t.Errorf("under the bound: %v", err)
	}
	for _, group := range []bool{false, true} {
		if _, _, err := sniff(laced(1025, 256, 1, group), lim); err == nil || err.(*proto.Error).Code != proto.CodeLimit {
			t.Errorf("over the bound (group %v): %v", group, err)
		}
	}
	// a request lowers the bound: 1000 blocks are 256,000 frames
	low := lim
	low.MaxTrackSamples = 100000
	if _, _, err := sniff(laced(1000, 256, 1, false), low); err == nil {
		t.Error("a lowered MaxTrackSamples is not applied to laced frames")
	}
	// three tracks each under the bound, together over twice it
	if _, _, err := sniff(laced(3000, 256, 3, false), lim); err == nil {
		t.Error("the total over twice the track bound is accepted")
	}
}

// The bomb of go-avkit/avkit#120: a file of a few KB of laced blocks, which a
// reader that unlaces up front would turn into hundreds of MiB. It is refused
// as video.limit before the reader runs, and the refusal allocates next to
// nothing, under a memory limit.
func TestLacedBlockBombsAreRefused(t *testing.T) {
	huge := proto.Limits{MaxBytes: math.MaxInt, MaxTrackSamples: math.MaxInt}
	for _, tc := range []struct {
		name           string
		blocks, tracks int
		group          bool
	}{
		{"10000 laced blocks", 10000, 1, false},
		{"10000 laced block groups", 10000, 1, true},
		{"10000 laced blocks, 3 tracks", 10000, 3, false},
		{"1000 laced blocks, lowered", 1000, 1, false},
	} {
		lim := huge
		if tc.name == "1000 laced blocks, lowered" {
			lim = proto.Limits{MaxTrackSamples: 100000}
		}
		bomb(t, tc.name, laced(tc.blocks, 256, tc.tracks, tc.group), lim, proto.CodeLimit, 1<<20)
	}
}
