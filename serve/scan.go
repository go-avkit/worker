// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package serve

import (
	"bytes"
	"encoding/binary"
	"fmt"

	"github.com/go-avkit/worker/proto"
)

func refuse(code, format string, a ...any) error {
	return &proto.Error{Code: code, Msg: fmt.Sprintf(format, a...)}
}

// sniff checks a file against the limits that need no container reader (its
// size, the number and nesting of its boxes or elements) and says what it is
// by its first bytes. These run before the container reader is asked anything,
// whatever the client said of the file.
func sniff(data []byte, lim proto.Limits) (format, mime string, err error) {
	if len(data) > lim.MaxBytes {
		return "", "", refuse(proto.CodeLimit, "the file is %d bytes; at most %d are read", len(data), lim.MaxBytes)
	}
	switch {
	case len(data) >= 12 && string(data[4:8]) == "ftyp":
		return "mp4", "video/mp4", scanBoxes(data, lim)
	case len(data) >= 4 && bytes.Equal(data[:4], []byte{0x1A, 0x45, 0xDF, 0xA3}):
		return "webm", "video/webm", scanEBML(data, lim)
	}
	return "", "", refuse(proto.CodeSyntax, "not an MP4 or WebM file")
}

// mp4Containers are the boxes whose children are boxes (ISO/IEC 14496-12:
// moov, trak, mdia, minf, stbl, dinf, edts, udta, mvex, moof, traf, mfra, sinf,
// schi). meta, a full box, holds boxes after four bytes.
var mp4Containers = map[string]bool{
	"moov": true, "trak": true, "mdia": true, "minf": true, "stbl": true, "dinf": true, "edts": true,
	"udta": true, "mvex": true, "moof": true, "traf": true, "mfra": true, "sinf": true, "schi": true, "meta": true,
}

// scanBoxes walks the boxes without decoding any: it counts them and measures
// their nesting, so that a file made of millions of empty boxes is refused
// before the container reader builds a tree of them.
func scanBoxes(data []byte, lim proto.Limits) error {
	count := 0
	var walk func(b []byte, depth int) error
	walk = func(b []byte, depth int) error {
		if depth > lim.MaxDepth {
			return refuse(proto.CodeLimit, "boxes nest deeper than %d", lim.MaxDepth)
		}
		for len(b) >= 8 {
			if count++; count > lim.MaxBoxes {
				return refuse(proto.CodeLimit, "the file holds more than %d boxes", lim.MaxBoxes)
			}
			size, hdr := uint64(binary.BigEndian.Uint32(b)), 8
			typ := string(b[4:8])
			if size == 1 {
				if len(b) < 16 {
					return nil
				}
				size, hdr = binary.BigEndian.Uint64(b[8:]), 16
			} else if size == 0 {
				size = uint64(len(b))
			}
			if size < uint64(hdr) || size > uint64(len(b)) {
				return nil // truncated or malformed: the container reader judges
			}
			if mp4Containers[typ] {
				body := b[hdr:size]
				if typ == "meta" {
					if len(body) < 4 {
						body = nil
					} else {
						body = body[4:]
					}
				}
				if err := walk(body, depth+1); err != nil {
					return err
				}
			}
			b = b[size:]
		}
		return nil
	}
	return walk(data, 1)
}

// ebmlMasters are the Matroska elements whose children are elements: Segment,
// SeekHead, Info, Tracks, TrackEntry, Video, Cluster, BlockGroup, Cues,
// CuePoint, CueTrackPositions, Tags, Tag, Chapters, EditionEntry, ChapterAtom,
// BlockAdditions, Attachments.
var ebmlMasters = map[uint32]bool{
	0x18538067: true, 0x114D9B74: true, 0x1549A966: true, 0x1654AE6B: true, 0xAE: true, 0xE0: true,
	0x1F43B675: true, 0xA0: true, 0x1C53BB6B: true, 0xBB: true, 0xB7: true, 0x1254C367: true, 0x7373: true,
	0x1043A770: true, 0x45B9: true, 0xB6: true, 0x75A1: true, 0x1941A469: true, 0x61A7: true, 0x4DBB: true,
}

func ebmlVint(b []byte, keepMarker bool) (v uint64, n int) {
	if len(b) == 0 || b[0] == 0 {
		return 0, 0
	}
	n = 1
	for m := byte(0x80); b[0]&m == 0; m >>= 1 {
		n++
	}
	if n > len(b) || n > 8 {
		return 0, 0
	}
	v = uint64(b[0])
	if !keepMarker {
		v &= 0xFF >> uint(n)
	}
	for _, c := range b[1:n] {
		v = v<<8 | uint64(c)
	}
	return v, n
}

// The Matroska block elements: a SimpleBlock, and the Block of a BlockGroup.
const (
	idSimpleBlock = 0xA3
	idBlock       = 0xA1
)

// blockFrames is the number of frames a block carries, from its header alone
// (Matroska, "Block Lacing"): the track number (a vint), a 16-bit timecode, a
// flags byte whose bits 0x06 name the lacing (none, Xiph, fixed-size, EBML), and
// for a laced block a byte holding the number of frames less one. A reader
// unlaces every block up front, so a 7-byte block can stand for 256 frames
// (go-avkit/avkit#120). ok is false for a block too short to say; the container
// reader judges it.
func blockFrames(body []byte) (track uint64, frames int, ok bool) {
	track, n := ebmlVint(body, false)
	if n == 0 || len(body) < n+3 {
		return 0, 0, false
	}
	if body[n+2]&0x06 == 0 {
		return track, 1, true
	}
	if len(body) < n+4 {
		return 0, 0, false
	}
	return track, int(body[n+3]) + 1, true
}

// scanEBML counts and nests the elements as scanBoxes does the boxes, and
// counts the frames the blocks hold, laced ones included: at most
// lim.MaxTrackSamples for one track and twice that for the file, since the
// container reader builds a record for every frame before the first poster is
// asked for.
func scanEBML(data []byte, lim proto.Limits) error {
	count, total := 0, 0
	perTrack := map[uint64]int{}
	var walk func(b []byte, depth int) error
	walk = func(b []byte, depth int) error {
		if depth > lim.MaxDepth {
			return refuse(proto.CodeLimit, "elements nest deeper than %d", lim.MaxDepth)
		}
		for len(b) > 0 {
			if count++; count > lim.MaxBoxes {
				return refuse(proto.CodeLimit, "the file holds more than %d elements", lim.MaxBoxes)
			}
			id, n := ebmlVint(b, true)
			if n == 0 {
				return nil
			}
			size, m := ebmlVint(b[n:], false)
			if m == 0 {
				return nil
			}
			body := b[n+m:]
			unknown := size == (uint64(1)<<(7*uint(m)))-1
			if unknown || size > uint64(len(body)) {
				size = uint64(len(body)) // unknown or cut short: to the end
			}
			if ebmlMasters[uint32(id)] {
				if err := walk(body[:size], depth+1); err != nil {
					return err
				}
			} else if id == idSimpleBlock || id == idBlock {
				if track, frames, ok := blockFrames(body[:size]); ok {
					perTrack[track] += frames
					total += frames
					if perTrack[track] > lim.MaxTrackSamples {
						return refuse(proto.CodeLimit, "track %d holds more than %d frames (laced blocks counted)", track, lim.MaxTrackSamples)
					}
					if total > 2*lim.MaxTrackSamples {
						return refuse(proto.CodeLimit, "the file holds more than %d frames (laced blocks counted)", 2*lim.MaxTrackSamples)
					}
				}
			}
			b = body[size:]
		}
		return nil
	}
	return walk(data, 1)
}
