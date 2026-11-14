# worker

[![ci](https://github.com/go-avkit/worker/actions/workflows/ci.yml/badge.svg)](https://github.com/go-avkit/worker/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/go-avkit/worker.svg)](https://pkg.go.dev/github.com/go-avkit/worker)
[![License: BSD-3-Clause](https://img.shields.io/badge/license-BSD--3--Clause-blue.svg)](LICENSE)

A video worker for [avkit](https://github.com/go-avkit/avkit): a program that
reads video files for a page that must not link a container reader or a
decoder. It answers two requests, **poster** (a JPEG of the frame at time *t*)
and **probe** (container, codec, size, duration), with go-avkit's container
reader (MP4, WebM / Matroska) and [H.264 decoder](https://github.com/go-avkit/decode).
Part of [go-avkit/avkit#89](https://github.com/go-avkit/avkit/issues/89).

| Package | What it is |
|---|---|
| `proto` | The request/response protocol. Standard library only: no `syscall/js`, no decoder. A client and a server both import it. |
| `serve` | `Server.Handle(req, payload)`: the server. Links go-avkit/decode and go-avkit/container. Pure Go, tested natively. |
| `cmd/avkit-worker` | The `js/wasm` Web Worker main (about 80 lines): a MessagePort in front of `serve`. |

## Using it from a host

Build the worker once, serve the `.wasm` next to Go's `wasm_exec.js`:

```sh
GOOS=js GOARCH=wasm go build -trimpath -ldflags="-s -w" -o avkit-worker.wasm ./cmd/avkit-worker
```

A host loads it in a Web Worker (`importScripts("wasm_exec.js")`, then
`WebAssembly.instantiateStreaming` and `go.run`), waits for `{"ready":true}`,
and gives it one end of a `MessageChannel`:

```js
const ch = new MessageChannel();
worker.postMessage({ videoPort: ch.port2 }, [ch.port2]);
ch.port1.postMessage({ json: JSON.stringify(req), data: fileArrayBuffer });
ch.port1.onmessage = (e) => { const res = JSON.parse(e.data.json); /* e.data.data: the JPEG */ };
```

Each message is `{json, data}`: the JSON header, and beside it the binary payload
(the file in a request, the poster's JPEG in a response). The transport is the
host's: another host can call `serve.Server.Handle` directly.

## The protocol

A **request** header (`proto.Request`):

| Field | Meaning |
|---|---|
| `id` | pairs the answer with the request |
| `op` | `"poster"` or `"probe"` |
| `key` | the file's version: `proto.Fingerprint`, its length and CRC-64/ECMA (at most 128 bytes) |
| `hasData` | the payload is the file; otherwise the worker's copy named by `key` is read |
| `format` | `"mp4"` or `"webm"`, informative (the worker sniffs again) |
| `t` | the poster's time, in seconds |
| `limits` | the bounds asked for (see below) |

A **response** header (`proto.Response`): `id`; on a refusal `code` and `error`;
otherwise `format`, `mime`, `codec`, `width`, `height`, `duration`, `decodable`,
and for a poster `image` (`"image/jpeg"`, the payload; its size is `width` x `height`).

The worker keeps the last 2 files it was sent, by `key`. A request for a file
it does not hold is answered `video.notheld` (never shown to a user); the client
sends it again with `hasData`.

| Code | Meaning |
|---|---|
| `video.limit` | the file, its boxes or elements, its frame, or the work of a poster is over a bound |
| `video.syntax` | not an MP4 or WebM, a container that cannot be read, no video track, a bad time, a bad request |
| `video.codec` | the poster cannot be decoded: a codec other than H.264 (the file still probes), or an H.264 tool that is not decoded |
| `video.unavailable` | the client could not reach a worker; a `Server` never answers it |
| `video.notheld` | the key names no held file: send it again with the data |

The wire format is that of go-slides' `video` client: the limits' field names are
those of its `video.Limits`.

## Limits: a request can only lower them

The worker's own bounds are `proto.DefaultLimits`: 64 MiB file, 262144 boxes,
nesting depth 16, 4096 x 4096 frame, 600 samples per poster, 262144 samples per
track, 8 MiB per sample, 1280 pixels of poster width. `proto.Clamp` is the limits
applied for a request, field by field:

- a value from 1 to the default is a lowering and is kept;
- a value above the default (`MaxInt`, 2^40), `0` and a negative mean **use the
  default**: never a refusal, never the value asked.

So for every request, every effective bound is in 1..default. This is a test
(`TestClampOnlyLowers`, `TestClampReviewerTable`), a fuzz property over all nine
fields (`FuzzClampOnlyLowers`, and `FuzzDecodeRequest` for any header), and the
bomb tests of `serve`. A held file is checked again against the limits of each
request that reads it.

## Security

A header and a file are untrusted. The header is bounded (64 KiB), and a
payload is bounded before it is copied (the worker refuses a file over 64 MiB on
its length alone). Before the container reader builds anything, a pre-scan counts
the boxes (MP4) or elements (Matroska), measures their nesting, and, for
Matroska, counts the **frames** the blocks hold: it reads each SimpleBlock and
Block's lacing header, because the reader unlaces every block up front and a
7-byte laced block stands for 256 frames
([go-avkit/avkit#120](https://github.com/go-avkit/avkit/issues/120)). More than
`MaxTrackSamples` frames in a track, or twice that in the file, is `video.limit`
(a 70 KB file of 10,000 laced blocks is refused with under 3 KB allocated). The
MP4 sample tables are not pre-scanned: the reader's own bounds apply there.

A panic in the reader or the decoder is answered as a `video.syntax` refusal, in
`Server.Handle` and again at the worker boundary, and the worker keeps serving.
The message types the page sends are checked: a payload that is not an
ArrayBuffer is refused, and a message that is not an object, or a `videoPort`
that is not a port, is ignored, not a crash.

**A file just under the cap costs memory.** The reader builds its tables up
front: the reviewer measured a 7 KB file of valid structure at 96 MiB allocated,
35 MiB retained per held file, so about 160 MiB with the 2 files held. That cost
is bounded by the frame and sample limits above, not by the file's size.

**Decode time is not bounded by a clock.** A poster is bounded by samples
(`MaxSamples`, 600), not by time: a 4096 x 4096, 600-frame, single-GOP H.264 file
took 12.4 s for one poster natively (indicative, machine at load 12). The decoder
has no deadline hook; a page should treat a poster request as slow and keep its
own timeout. Lower `MaxSamples`, `MaxWidth` and `MaxHeight` in the request to
shorten the worst case.

Fuzz targets: `FuzzDecodeRequest`, `FuzzClampOnlyLowers`, `FuzzHandle` (all nine
limits, hostile payloads; corpus in `testdata/fuzz`); the bomb tests
(`TestBombsAreRefusedWithinMemory`, `TestLacedBlockBombsAreRefused`) assert a
refusal and a small allocation, and CI runs the suite under `GOMEMLIMIT`.
The test videos in `serve/testdata` are the small files of go-slides' video tests.

## Size

`cmd/avkit-worker` built with Go 1.27.4 (`-trimpath -ldflags="-s -w"`, js/wasm):
12,881,038 bytes raw, 3,297,044 bytes gzip -9 (12.3 MiB, 3.1 MiB). Serve it
compressed.

## Licence

BSD-3-Clause, see [LICENSE](LICENSE).
