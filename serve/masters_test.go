// Copyright (c) 2026, go-avkit
// SPDX-License-Identifier: BSD-3-Clause

package serve

import (
	"math"
	"testing"

	"github.com/at-wat/ebml-go"
	"github.com/go-avkit/worker/proto"
)

func idOf(t ebml.ElementType) uint32 {
	var v uint32
	for _, c := range t.Bytes() {
		v = v<<8 | uint32(c)
	}
	return v
}

// The scan's master set and ebml-go's element table agree, element by element:
// an element is in the set if and only if the table says it is a Master. A
// named element that is missing from the set is a place a block can hide in.
func TestMastersAgreeWithEbmlGoTable(t *testing.T) {
	n := 0
	for et := ebml.ElementInvalid + 1; et < 4096; et++ {
		if len(et.Bytes()) == 0 {
			continue
		}
		n++
		if got, want := ebmlMasters[idOf(et)], et.DataType() == ebml.DataTypeMaster; got != want {
			t.Errorf("%s (%#x): in the set %v, Master in the table %v", et, idOf(et), got, want)
		}
	}
	if n < 100 {
		t.Fatalf("only %d elements enumerated: the table is not being read", n)
	}
	// the ids the review named, and the hand-written list this set replaced
	for _, id := range []uint32{
		0x41E4, 0x8E, 0xA6, 0xE8, // BlockAdditionMapping, Slices, BlockMore, TimeSlice
		0x18538067, 0x114D9B74, 0x1549A966, 0x1654AE6B, 0xAE, 0xE0, 0x1F43B675, 0xA0, 0x1C53BB6B, 0xBB, 0xB7,
		0x1254C367, 0x7373, 0x1043A770, 0x45B9, 0xB6, 0x75A1, 0x1941A469, 0x61A7, 0x4DBB,
	} {
		if !ebmlMasters[id] {
			t.Errorf("%#x is not a master", id)
		}
	}
	// the block elements are leaves for the scan, and counted
	if ebmlMasters[idSimpleBlock] || ebmlMasters[idBlock] {
		t.Error("a block is a master")
	}
	if ebml.ElementSimpleBlock.DataType() != ebml.DataTypeBlock || idOf(ebml.ElementSimpleBlock) != idSimpleBlock ||
		ebml.ElementBlock.DataType() != ebml.DataTypeBlock || idOf(ebml.ElementBlock) != idBlock {
		t.Error("the block ids of the scan are not ebml-go's")
	}
}

// laced blocks hidden in a master that a hand-written list lacked are counted:
// refused as video.limit, with next to nothing allocated.
func TestLacedBlocksInOtherMastersAreCounted(t *testing.T) {
	huge := proto.Limits{MaxBytes: math.MaxInt, MaxBoxes: math.MaxInt, MaxTrackSamples: math.MaxInt}
	for name, master := range map[string][]byte{
		"BlockAdditionMapping": {0x41, 0xE4},
		"BlockMore":            {0xA6},
		"Slices":               {0x8E},
		"TimeSlice":            {0xE8},
	} {
		d := append(append([]byte{}, ebmlHead...), 0x18, 0x53, 0x80, 0x67)
		d = append(append(d, unknown...), 0x1F, 0x43, 0xB6, 0x75)
		d = append(append(d, unknown...), master...)
		d = append(d, unknown...)
		for range 10000 {
			d = append(d, block(idSimpleBlock, 1, 0x04, 256)...)
		}
		bomb(t, "10000 laced blocks in "+name, d, huge, proto.CodeLimit, 1<<20)
	}
}
