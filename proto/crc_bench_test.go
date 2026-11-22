package proto

import (
	stdcrc64 "hash/crc64"
	"testing"

	simdcrc64 "github.com/go-simd/crc64"
)

var crcBenchSizes = []struct {
	name string
	n    int
}{
	{"16B", 16}, {"64B", 64}, {"256B", 256}, {"1KiB", 1 << 10},
	{"4KiB", 4 << 10}, {"64KiB", 64 << 10}, {"1MiB", 1 << 20}, {"10MiB", 10 << 20},
}

var crcSink uint64

func BenchmarkCRC64(b *testing.B) {
	stdTab := stdcrc64.MakeTable(stdcrc64.ECMA)
	simdTab := simdcrc64.MakeTable(simdcrc64.ECMA)
	for _, s := range crcBenchSizes {
		data := make([]byte, s.n)
		for i := range data {
			data[i] = byte(i*131 + 7)
		}
		if stdcrc64.Checksum(data, stdTab) != simdcrc64.Checksum(data, simdTab) {
			b.Fatalf("%s: checksums differ", s.name)
		}
		b.Run("std/"+s.name, func(b *testing.B) {
			b.SetBytes(int64(s.n))
			for i := 0; i < b.N; i++ {
				crcSink = stdcrc64.Checksum(data, stdTab)
			}
		})
		b.Run("simd/"+s.name, func(b *testing.B) {
			b.SetBytes(int64(s.n))
			for i := 0; i < b.N; i++ {
				crcSink = simdcrc64.Checksum(data, simdTab)
			}
		})
	}
}
