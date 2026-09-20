// Package fcut splits a binary file into fixed-size chunk parts backed by a
// JSON manifest that records the part count and a per-part checksum, and joins
// the parts back exactly, verifying every part's size and checksum before the
// joined output is finalized. It uses only the Go standard library and works
// fully offline.
//
// Splitting streams the source file one chunk at a time and writes parts named
// <base>XXXXX.part, where the width (5 or more digits) matches the total part
// count. Joining reads every part in order, verifies its size and checksum,
// streams it into a temporary file, and atomically renames that file into place
// only after every part has passed verification — so the output path is never
// touched on failure (see Join for the full policy).
package fcut

import (
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// SchemaVersion is the only manifest schema LoadManifest accepts.
const SchemaVersion = "fcut-manifest-1"

// ManifestFileName is the name Split gives the manifest inside destDir.
const ManifestFileName = "manifest.json"

const (
	// AlgoSHA256 is the default checksum algorithm. It satisfies algebraically
	// fdc-safe file verification for the splitting/joining use case.
	AlgoSHA256 = "sha256"

	// AlgoMD5 is the optional, weaker checksum algorithm (opt in with
	// --algo md5). MD5 is not cryptographically collision-resistant; prefer
	// sha256 unless you need MD5 for legacy interop.
	AlgoMD5 = "md5"
)

// ErrHashMismatch is returned by Join when a part fails size or checksum
// verification. It is a sentinel: use errors.Is(err, fcut.ErrHashMismatch).
// The returned error wraps this sentinel with the offending part number and
// file name plus the details of the mismatch.
var ErrHashMismatch = errors.New("fcut: part verification failed")

// Part records one chunk file in a manifest.
//
// The digest slot is named sha256 because that is the field the v1 manifest
// schema defines; the digest stored there is computed with the algorithm
// selected at split time (sha256 by default, md5 with Algo md5). The
// manifest's Algo field records which one was used. An all-zero digest means
// the split ran with checksums disabled and Join will skip hash verification
// for that part (see SplitOpts.NoChecksums).
type Part struct {
	File   string `json:"file"`   // part file name, e.g. sample00001.part
	Size   int64  `json:"size"`   // exact byte count of this part
	SHA256 string `json:"sha256"` // lowercase hex digest, or zero digest if disabled
}

// Manifest is the JSON document produced by Split and consumed by Join and
// LoadManifest.
type Manifest struct {
	Schema    string `json:"schema"`         // always SchemaVersion
	Source    string `json:"source"`         // source path as given to Split
	TotalSize int64  `json:"total_size"`     // sum of all part sizes
	ChunkSize int64  `json:"chunk_size"`     // requested chunk size in bytes
	Algo      string `json:"algo,omitempty"` // checksum algorithm; omitted = sha256
	Parts     []Part `json:"parts"`          // part records in split order
	CreatedAt string `json:"created_at"`     // RFC3339 UTC timestamp
}

// SplitOpts configures Split.
type SplitOpts struct {
	// Algo selects the per-part checksum algorithm: "sha256" (default) or
	// "md5". Empty means sha256. Comparisons are case-insensitive.
	Algo string

	// NoChecksums disables checksum computation: every part gets an all-zero
	// digest in the manifest and Join will skip hash verification for those
	// parts (size checks still run). Use only when you understand the risk —
	// a corrupt part will not be detected. Default false.
	NoChecksums bool
}

// JoinOpts configures Join.
type JoinOpts struct {
	// Algo overrides the algorithm used to verify parts. Empty means use the
	// algorithm recorded in the manifest (sha256 when the manifest records
	// none). Comparisons are case-insensitive.
	Algo string
}

// Split reads src in streaming chunks of exactly chunkSize bytes (the final
// chunk may be shorter) and writes chunk files named <base>XXXXX.part into
// destDir, where base is the source's file name without its extension and the
// index is zero-padded to the number of digits of the total part count (always
// at least 5). It then writes destDir/manifest.json (see ManifestFileName)
// recording the part count, sizes and per-part checksums, and returns the
// manifest.
//
// An empty source file produces zero parts and a manifest with total_size 0;
// joining such a manifest yields an empty output file.
//
// chunkSize must be >= 1, otherwise Split returns an error. If the source file
// is modified while splitting, Split fails and does not save a manifest.
// Existing files in destDir are left alone; parts are written (overwritten) by
// name, so a previous split of the same base into the same directory is
// replaced.
func Split(src string, chunkSize int, destDir string, opts SplitOpts) (*Manifest, error) {
	if chunkSize < 1 {
		return nil, fmt.Errorf("fcut: chunk size must be >= 1, got %d", chunkSize)
	}
	algo, err := normalizeAlgo(opts.Algo)
	if err != nil {
		return nil, err
	}

	f, err := os.Open(src)
	if err != nil {
		return nil, fmt.Errorf("open source %s: %w", src, err)
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat source %s: %w", src, err)
	}
	size := fi.Size()

	if size == 0 {
		m := &Manifest{
			Schema:    SchemaVersion,
			Source:    src,
			TotalSize: 0,
			ChunkSize: int64(chunkSize),
			Parts:     []Part{},
			CreatedAt: time.Now().UTC().Format(time.RFC3339),
		}
		if algo != AlgoSHA256 {
			m.Algo = algo
		}
		if err := SaveManifest(m, filepath.Join(destDir, ManifestFileName)); err != nil {
			return nil, err
		}
		return m, nil
	}

	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return nil, fmt.Errorf("create destination %s: %w", destDir, err)
	}

	expected := (size + int64(chunkSize) - 1) / int64(chunkSize)
	width := len(strconv.FormatInt(expected, 10))
	if width < 5 {
		width = 5
	}

	base := partBase(src)
	parts := make([]Part, 0, expected)

	var writtenTotal int64
	for i := int64(0); i < expected; i++ {
		name := fmt.Sprintf("%s%0*d.part", base, width, i+1)
		partPath := filepath.Join(destDir, name)

		out, err := os.OpenFile(partPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
		if err != nil {
			return nil, fmt.Errorf("create part %s: %w", name, err)
		}

		h := newHasher(algo)
		n, err := io.CopyN(io.MultiWriter(out, h), f, int64(chunkSize))
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		if err != nil && err != io.EOF {
			return nil, fmt.Errorf("split source into %s: %w", name, err)
		}
		if n == 0 && err == io.EOF {
			break
		}

		digest := ""
		if opts.NoChecksums {
			digest = strings.Repeat("0", hexLength(algo))
		} else {
			digest = hex.EncodeToString(h.Sum(nil))
		}
		parts = append(parts, Part{File: name, Size: n, SHA256: digest})
		writtenTotal += n

		if err == io.EOF {
			break
		}
	}

	if writtenTotal != size {
		return nil, fmt.Errorf("fcut: source %s changed while splitting: wrote %d bytes of %d",
			src, writtenTotal, size)
	}

	m := &Manifest{
		Schema:    SchemaVersion,
		Source:    src,
		TotalSize: size,
		ChunkSize: int64(chunkSize),
		Parts:     parts,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	if algo != AlgoSHA256 {
		m.Algo = algo
	}
	if err := SaveManifest(m, filepath.Join(destDir, ManifestFileName)); err != nil {
		return nil, err
	}
	return m, nil
}

// LoadManifest reads and validates a manifest file. It rejects files whose
// schema is not SchemaVersion, manifests with chunk_size < 1 or negative
// total_size, and parts with empty names or negative sizes.
func LoadManifest(path string) (*Manifest, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read manifest %s: %w", path, err)
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("parse manifest %s: %w", path, err)
	}
	if m.Schema != SchemaVersion {
		return nil, fmt.Errorf("invalid manifest %s: schema %q, want %q", path, m.Schema, SchemaVersion)
	}
	if m.ChunkSize < 1 {
		return nil, fmt.Errorf("invalid manifest %s: chunk_size %d must be >= 1", path, m.ChunkSize)
	}
	if m.TotalSize < 0 {
		return nil, fmt.Errorf("invalid manifest %s: total_size %d must be >= 0", path, m.TotalSize)
	}
	if m.Parts == nil {
		m.Parts = []Part{}
	}
	for i, p := range m.Parts {
		if p.File == "" {
			return nil, fmt.Errorf("invalid manifest %s: part %d has an empty file name", path, i+1)
		}
		if p.Size < 0 {
			return nil, fmt.Errorf("invalid manifest %s: part %d (%s) has negative size %d",
				path, i+1, p.File, p.Size)
		}
	}
	return &m, nil
}

// SaveManifest writes m as indented JSON to path, creating the parent
// directory if needed.
func SaveManifest(m *Manifest, path string) error {
	if m == nil {
		return errors.New("fcut: cannot save a nil manifest")
	}
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("encode manifest: %w", err)
	}
	b = append(b, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create directory for manifest: %w", err)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		return fmt.Errorf("write manifest %s: %w", path, err)
	}
	return nil
}

// Join rebuilds the file described by manifestPath into outPath. Each part is
// read in manifest order, checked against its recorded size, streamed through
// a hash (unless its manifest digest is all zeros, i.e. checksums were
// disabled at split time), and compared against the recorded digest.
//
// Verification policy: nothing is written to outPath itself until every part
// has passed. Parts are streamed into a temporary file in the output
// directory, which is atomically renamed to outPath only on success. On any
// failure — size mismatch, checksum mismatch, missing or unreadable part —
// Join returns an error, the temporary file is removed, and outPath is left
// untouched (if outPath already existed it is left as it was). Mismatches
// return an error wrapping ErrHashMismatch whose message identifies the part
// number and file (use errors.Is). Part files named without a directory are
// resolved relative to the manifest's directory, so the chunks and their
// manifest can be moved around together.
func Join(manifestPath, outPath string, opts JoinOpts) error {
	m, err := LoadManifest(manifestPath)
	if err != nil {
		return err
	}
	algo := strings.ToLower(strings.TrimSpace(opts.Algo))
	if algo == "" {
		algo = strings.ToLower(strings.TrimSpace(m.Algo))
	}
	if algo == "" {
		algo = AlgoSHA256
	}
	algo, err = normalizeAlgo(algo)
	if err != nil {
		return err
	}

	if len(m.Parts) == 0 {
		if m.TotalSize != 0 {
			return fmt.Errorf("%s: manifest has no parts but total_size %d", manifestPath, m.TotalSize)
		}
		out, err := os.OpenFile(outPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
		if err != nil {
			return fmt.Errorf("create output %s: %w", outPath, err)
		}
		if err := out.Close(); err != nil {
			return fmt.Errorf("finalize output %s: %w", outPath, err)
		}
		return nil
	}

	dir := filepath.Dir(outPath)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create output directory %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".fcut-join-*")
	if err != nil {
		return fmt.Errorf("create output %s: %w", outPath, err)
	}
	tmpName := tmp.Name()
	cleanup := func() {
		tmp.Close()
		os.Remove(tmpName) // no-op after a successful rename
	}
	ok := false
	defer func() {
		if !ok {
			cleanup()
		}
	}()

	manifestDir := filepath.Dir(manifestPath)
	var written int64
	for i, p := range m.Parts {
		name := p.File
		partPath := name
		if !filepath.IsAbs(partPath) {
			partPath = filepath.Join(manifestDir, name)
		}

		pf, err := os.Open(partPath)
		if err != nil {
			return fmt.Errorf("part %d (%s): %w", i+1, name, err)
		}
		fi, err := pf.Stat()
		if err != nil {
			pf.Close()
			return fmt.Errorf("part %d (%s): stat: %w", i+1, name, err)
		}
		if fi.Size() != p.Size {
			pf.Close()
			return fmt.Errorf("%w: part %d (%s): size mismatch: got %d bytes, manifest says %d",
				ErrHashMismatch, i+1, name, fi.Size(), p.Size)
		}

		if allZeroDigest(p.SHA256) {
			n, copyErr := io.Copy(tmp, pf)
			pf.Close()
			if copyErr != nil {
				return fmt.Errorf("part %d (%s): copy: %w", i+1, name, copyErr)
			}
			if n != p.Size {
				return fmt.Errorf("%w: part %d (%s): size mismatch after copy: got %d bytes of %d",
					ErrHashMismatch, i+1, name, n, p.Size)
			}
			written += n
			continue
		}

		h := newHasher(algo)
		n, copyErr := io.Copy(tmp, io.TeeReader(pf, h))
		pf.Close()
		if copyErr != nil {
			return fmt.Errorf("part %d (%s): copy: %w", i+1, name, copyErr)
		}
		if n != p.Size {
			return fmt.Errorf("%w: part %d (%s): size mismatch after copy: got %d bytes of %d",
				ErrHashMismatch, i+1, name, n, p.Size)
		}
		got := hex.EncodeToString(h.Sum(nil))
		want := strings.ToLower(strings.TrimSpace(p.SHA256))
		if !strings.EqualFold(got, want) {
			return fmt.Errorf("%w: part %d (%s): %s mismatch: got %s, manifest says %s",
				ErrHashMismatch, i+1, name, algo, got, p.SHA256)
		}
		written += n
	}

	if written != m.TotalSize {
		return fmt.Errorf("%s: part sizes sum to %d bytes but total_size is %d",
			manifestPath, written, m.TotalSize)
	}

	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("sync output %s: %w", outPath, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("finalize output %s: %w", outPath, err)
	}
	if err := os.Rename(tmpName, outPath); err != nil {
		return fmt.Errorf("finalize output %s: %w", outPath, err)
	}
	ok = true
	return nil
}

func allZeroDigest(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return true
	}
	return strings.Trim(s, "0") == ""
}

func normalizeAlgo(a string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(a)) {
	case "", "sha", "sha256", "sha-256":
		return AlgoSHA256, nil
	case "md5", "md-5":
		return AlgoMD5, nil
	default:
		return "", fmt.Errorf("fcut: unsupported checksum algorithm %q (want sha256 or md5)", a)
	}
}

func newHasher(algo string) hash.Hash {
	switch algo {
	case AlgoMD5:
		return md5.New()
	default:
		return sha256.New()
	}
}

func hexLength(algo string) int {
	if algo == AlgoMD5 {
		return 32
	}
	return 64
}

// partBase returns the chunk file name prefix for a source path: the base
// name with its final extension removed. "dir/data.tar.gz" gives "data.tar",
// "README" gives "README", ".bashrc" gives ".bashrc".
func partBase(path string) string {
	b := filepath.Base(path)
	ext := filepath.Ext(b)
	if ext != "" && ext != b {
		return strings.TrimSuffix(b, ext)
	}
	return b
}
