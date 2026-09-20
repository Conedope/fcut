# fcut

A file splitter/joiner CLI written in pure Go (standard library only, fully
offline). `fcut split` cuts a binary file into fixed-size chunk parts backed by
a JSON manifest that records the part count and a per-part checksum;
`fcut join` rebuilds the exact original file, verifying every part's size and
checksum before the output is finalized.

![Go](https://img.shields.io/badge/Go-1.22+-00ADD8) ![License](https://img.shields.io/badge/License-MIT-blue)

```
$ fcut --version
fcut 1.0.0
```

## Build

```
go build -o fcut ./cmd/fcut
```

Requires Go 1.22+. The library lives at the module root
(`github.com/Conedope/fcut`) and imports only the standard library.

## Quick start

Split a file into 3 KiB parts:

```
$ fcut split backup.tar -s 3k -d chunks
manifest: chunks/manifest.json
3 parts, 7778 bytes
```

```
$ ls chunks
backup00001.part  backup00002.part  backup00003.part  manifest.json
```

Join them back and verify:

```
$ fcut join chunks/manifest.json -o restored.tar
joined 7778 bytes, 3 parts verified

$ cmp backup.tar restored.tar && echo identical
identical
```

Each part's size and checksum is verified before anything is written to the
output path; if any part is missing or corrupt the join fails cleanly:

```
$ fcut join chunks/manifest.json -o restored.tar
fcut join: fcut: part verification failed: part 2 (backup00002.part): sha256 mismatch: got 85e5…, manifest says 4c52…
```

(Output above captured from a real run on this README.)

## Manifest

`Split` writes `DIR/manifest.json`, a `fcut-manifest-1` schema document. Part
file names are `<base>XXXXX.part`, where `base` is the source file name without
its extension and the index is zero-padded to the length of the total part
count (at least 5 digits). Real manifest from the quick start above:

```json
{
  "schema": "fcut-manifest-1",
  "source": "backup.tar",
  "total_size": 7778,
  "chunk_size": 3072,
  "parts": [
    {
      "file": "backup00001.part",
      "size": 3072,
      "sha256": "85e527408ceb826bb8353f79c56517bad56f1bcf77dcdb9d717194d60b034999"
    },
    {
      "file": "backup00002.part",
      "size": 3072,
      "sha256": "4c52875cc7e8f16993ddcf596d1388a406bfaa28f1a129fc0654f8c765eeefdf"
    },
    {
      "file": "backup00003.part",
      "size": 1634,
      "sha256": "947f89fc3aaa9862261b1ca4df9a480085a7451406cea1d40743c7d219e5b8da"
    }
  ],
  "created_at": "2026-09-20T00:24:10Z"
}
```

## Size suffixes

`-s SIZE` accepts an integer byte count with an optional suffix. Suffixes are
binary powers of two and case-insensitive:

| Suffix | Multiplier   | Example        | Bytes         |
|--------|--------------|----------------|---------------|
| *(none)* or `B` | 1            | `-s 512`       | 512           |
| `K`, `KB`, `KiB` | 2^10 (1024)  | `-s 64k`       | 65,536        |
| `M`, `MB`, `MiB` | 2^20         | `-s 1M`        | 1,048,576     |
| `G`, `GB`, `GiB` | 2^30         | `-s 2G`        | 2,147,483,648 |
| `T`, `TB`, `TiB` | 2^40         | `-s 1T`        | 1,099,511,627,776 |

## CLI reference

```
fcut split FILE -s SIZE [-d DIR] [--algo sha256|md5] [--no-checksums]
fcut join MANIFEST -o OUT [--algo sha256|md5]
fcut --version | --help
```

### `split`

- `-s, --size SIZE` — chunk size; required. Accepts the suffixes above.
- `-d, --dir DIR` — destination directory for parts and the manifest
  (default `.`).
- `-a, --algo NAME` — per-part checksum algorithm: `sha256` (default) or `md5`.
- `--no-checksums` — store **zero hashes** in the manifest instead of digests.
  Joins then skip hash verification (size checks still run), so a corrupt part
  is **not** detected. Use only when you accept that risk.

Prints the manifest path and a summary:

```
manifest: chunks/manifest.json
4 parts, 34 bytes
```

### `join`

- `-o, --output OUT` — output file; required.
- `-a, --algo NAME` — override the algorithm recorded in the manifest (default:
  the manifest's algorithm, `sha256` if none is recorded).

Prints a summary on success:

```
joined 34 bytes, 4 parts verified
```

### Exit codes

| Code | Meaning |
|------|---------|
| 0    | success |
| 1    | runtime failure: missing/unreadable file, split error, missing part, size or hash mismatch |
| 2    | usage error: unknown command, missing/invalid arguments, invalid size |

### Behavior notes

- The last part may be shorter than `chunk_size`; every other part is exactly
  `chunk_size` bytes.
- An **empty source** splits to **zero parts** and a manifest with
  `total_size: 0`; joining it produces an empty output file.
- Part files without a directory component are resolved **relative to the
  manifest's directory** on join, so a manifest + its parts tree can be moved
  around together.
- **Join atomicity:** parts stream into a temporary file in the output's
  directory which is atomically renamed to `OUT` only after every part is
  verified. On any mismatch, missing part or write error the temporary file is
  removed and `OUT` is left untouched.
- A digest that is empty or all zeros in the manifest (see `--no-checksums`)
  skips hash verification for that part; its size is still checked.
- With `--algo md5` the digest is stored in the manifest's `sha256` slot (the
  v1 schema field name) and the top-level `algo: "md5"` records the algorithm.
  MD5 is not cryptographically collision-resistant — prefer `sha256`.
- Existing files in the destination directory are not deleted; parts are
  overwritten by name.

## Library

```go
import "github.com/Conedope/fcut"

m, err := fcut.Split("backup.tar", 1<<20, "chunks", fcut.SplitOpts{})
// m.TotalSize, m.Parts[i].File / .Size / .SHA256

m, err = fcut.LoadManifest("chunks/manifest.json")

err = fcut.Join("chunks/manifest.json", "restored.tar", fcut.JoinOpts{})
if errors.Is(err, fcut.ErrHashMismatch) {
    // a part failed size or checksum verification; err names the part
}
```

- `Split(src string, chunkSize int, destDir string, opts SplitOpts) (*Manifest, error)`
  — streams `src` in chunks, writes `<base>XXXXX.part` files and
  `destDir/manifest.json`, returns the manifest. `chunkSize` must be >= 1.
- `LoadManifest(path string) (*Manifest, error)` — validates the `schema` and
  field constraints; rejects other schemas.
- `SaveManifest(m *Manifest, path string) error` — indented JSON.
- `Join(manifestPath, outPath string, opts JoinOpts) error` — verifies then
  joins; `ErrHashMismatch` is a sentinel usable with `errors.Is`.

## Design

**Splitting** reads the source in streaming `chunk_size` windows via
`io.CopyN` into a `MultiWriter(file, hash)`, so each part is written and hashed
in a single pass with no extra buffering of the whole file. The expected part
count comes from a stat; if the source changes size mid-split `Split` fails
and writes no manifest.

**Verification-first joining** computes each part's size and digest by
streaming before the result is released: parts go to a temp file in the
destination directory, and `os.Rename` atomically publishes `OUT` only after
the final part passes. This keeps the "no partial output on failure" guarantee
without buffering arbitrarily large files in memory.

## Tests

```
CGO_ENABLED=0 go vet ./...
CGO_ENABLED=0 go test ./... -v
```

Covered (library + CLI-through-binary): exact chunk boundaries and part naming
(10/10/10/4 on a 34-byte input, exact-multiple sizes, 6-digit names at
100,000 parts), manifest JSON contents and schema validation, byte-identical
join round-trips, corrupt-part (hash) and truncated-part (size) failures that
leave no output and satisfy `errors.Is(ErrHashMismatch)`, missing-part
failures, empty sources, zero-checksum mode, SHA-256 and MD5 algorithms,
size-suffix parsing, and full split→join round-trips plus exit codes (0/1/2)
against the built binary. The 100,000-part test dominates the library suite's
runtime (roughly a minute); use `go test -run` to skip it when iterating.

## License

MIT. See [LICENSE](LICENSE).