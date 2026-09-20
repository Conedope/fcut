package fcut

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func deterministicBytes(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte((i*31 + i/7 + 5) % 256)
	}
	return b
}

func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func mustSplit(t *testing.T, src string, chunk int, dir string, opts SplitOpts) *Manifest {
	t.Helper()
	m, err := Split(src, chunk, dir, opts)
	if err != nil {
		t.Fatalf("Split(%s, %d): %v", src, chunk, err)
	}
	return m
}

func TestSplitBoundaries(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "sample.dat")
	data := deterministicBytes(34)
	writeFile(t, src, data)

	parts := filepath.Join(dir, "chunks")
	m := mustSplit(t, src, 10, parts, SplitOpts{})

	if m.Schema != SchemaVersion {
		t.Errorf("schema = %q, want %q", m.Schema, SchemaVersion)
	}
	if m.TotalSize != 34 {
		t.Errorf("total_size = %d, want 34", m.TotalSize)
	}
	if m.ChunkSize != 10 {
		t.Errorf("chunk_size = %d, want 10", m.ChunkSize)
	}
	if m.Algo != "" {
		t.Errorf("default manifest should omit algo, got %q", m.Algo)
	}

	want := []struct {
		file string
		size int64
	}{
		{"sample00001.part", 10},
		{"sample00002.part", 10},
		{"sample00003.part", 10},
		{"sample00004.part", 4},
	}
	if len(m.Parts) != len(want) {
		t.Fatalf("got %d parts, want %d", len(m.Parts), len(want))
	}
	var got int64
	for i, p := range m.Parts {
		if p.File != want[i].file {
			t.Errorf("part %d file = %q, want %q", i+1, p.File, want[i].file)
		}
		if p.Size != want[i].size {
			t.Errorf("part %d size = %d, want %d", i+1, p.Size, want[i].size)
		}
		if len(p.SHA256) != 64 {
			t.Errorf("part %d sha256 len = %d, want 64 hex chars", i+1, len(p.SHA256))
		}
		got += p.Size
		mustOpen := filepath.Join(parts, p.File)
		fi, err := os.Stat(mustOpen)
		if err != nil {
			t.Errorf("part file %s missing: %v", p.File, err)
		} else if fi.Size() != p.Size {
			t.Errorf("on-disk size of %s = %d, want %d", p.File, fi.Size(), p.Size)
		}
	}
	if got != 34 {
		t.Errorf("sum of part sizes = %d, want 34", got)
	}

	mp := filepath.Join(parts, ManifestFileName)
	if _, err := os.Stat(mp); err != nil {
		t.Errorf("manifest file %s not written: %v", mp, err)
	}
}

func TestSplitExactBoundary(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "data.bin")
	writeFile(t, src, deterministicBytes(30))

	m := mustSplit(t, src, 10, filepath.Join(dir, "c1"), SplitOpts{})
	if len(m.Parts) != 3 {
		t.Fatalf("30 bytes at chunk 10: got %d parts, want 3", len(m.Parts))
	}
	for i, p := range m.Parts {
		if p.Size != 10 {
			t.Errorf("part %d size = %d, want 10", i+1, p.Size)
		}
	}

	src2 := filepath.Join(dir, "data2.bin")
	writeFile(t, src2, deterministicBytes(33))
	m2 := mustSplit(t, src2, 11, filepath.Join(dir, "c2"), SplitOpts{})
	if len(m2.Parts) != 3 {
		t.Fatalf("33 bytes at chunk 11: got %d parts, want 3", len(m2.Parts))
	}
	for _, p := range m2.Parts {
		if p.Size != 11 {
			t.Errorf("part size = %d, want 11", p.Size)
		}
	}
}

func TestSplitPartNamesMatchManifest(t *testing.T) {
	dir := t.TempDir()
	// 100000 bytes at chunk 1 forces 100000 parts => 6-digit names.
	src := filepath.Join(dir, "many.bin")
	writeFile(t, src, deterministicBytes(100000))

	m := mustSplit(t, src, 1, filepath.Join(dir, "c"), SplitOpts{})
	if len(m.Parts) != 100000 {
		t.Fatalf("got %d parts, want 100000", len(m.Parts))
	}
	if m.Parts[0].File != "many000001.part" {
		t.Errorf("first part file = %q, want many000001.part", m.Parts[0].File)
	}
	if m.Parts[len(m.Parts)-1].File != "many100000.part" {
		t.Errorf("last part file = %q, want many100000.part", m.Parts[len(m.Parts)-1].File)
	}
}

func TestSplitInvalidChunkSize(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "x.bin")
	writeFile(t, src, deterministicBytes(8))
	for _, n := range []int{0, -1, -100} {
		if _, err := Split(src, n, dir, SplitOpts{}); err == nil {
			t.Errorf("Split with chunkSize %d: want error, got nil", n)
		}
	}
}

func TestSplitEmptySource(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "empty.bin")
	writeFile(t, src, nil)

	parts := filepath.Join(dir, "chunks")
	m := mustSplit(t, src, 16, parts, SplitOpts{})
	if len(m.Parts) != 0 {
		t.Fatalf("empty source: got %d parts, want 0", len(m.Parts))
	}
	if m.TotalSize != 0 {
		t.Errorf("empty source: total_size = %d, want 0", m.TotalSize)
	}

	out := filepath.Join(dir, "restored.bin")
	if err := Join(filepath.Join(parts, ManifestFileName), out, JoinOpts{}); err != nil {
		t.Fatalf("Join of empty manifest: %v", err)
	}
	fi, err := os.Stat(out)
	if err != nil {
		t.Fatalf("Join of empty manifest produced no output: %v", err)
	}
	if fi.Size() != 0 {
		t.Errorf("joined empty file size = %d, want 0", fi.Size())
	}
}

func TestManifestSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "rt.bin")
	data := deterministicBytes(1000)
	writeFile(t, src, data)

	parts := filepath.Join(dir, "chunks")
	m := mustSplit(t, src, 333, parts, SplitOpts{})

	loaded, err := LoadManifest(filepath.Join(parts, ManifestFileName))
	if err != nil {
		t.Fatalf("LoadManifest: %v", err)
	}
	if loaded.Schema != m.Schema || loaded.Source != m.Source ||
		loaded.TotalSize != m.TotalSize || loaded.ChunkSize != m.ChunkSize {
		t.Log("manifest fields round-tripped inconsistently")
	}
	if len(loaded.Parts) != len(m.Parts) {
		t.Fatalf("part count = %d, want %d", len(loaded.Parts), len(m.Parts))
	}
	for i, p := range m.Parts {
		lp := loaded.Parts[i]
		if p.File != lp.File || p.Size != lp.Size || p.SHA256 != lp.SHA256 {
			t.Errorf("part %d round-trip: got %+v, want %+v", i+1, lp, p)
		}
	}
}

func TestLoadManifestRejectsBadSchema(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "bad.json")
	writeFile(t, p, []byte(`{"schema":"fcut-manifest-2","source":"x","total_size":0,"chunk_size":8,"parts":[]}`))
	if _, err := LoadManifest(p); err == nil || !strings.Contains(err.Error(), "schema") {
		t.Errorf("bad schema: want error naming schema, got %v", err)
	}
}

func TestLoadManifestRejectsMalformed(t *testing.T) {
	dir := t.TempDir()

	p1 := filepath.Join(dir, "notjson.json")
	writeFile(t, p1, []byte(`{nope`))
	if _, err := LoadManifest(p1); err == nil {
		t.Error("malformed JSON: want error")
	}

	p2 := filepath.Join(dir, "badchunk.json")
	writeFile(t, p2, []byte(`{"schema":"fcut-manifest-1","chunk_size":0,"parts":[]}`))
	if _, err := LoadManifest(p2); err == nil {
		t.Error("chunk_size 0: want error")
	}
}

func TestLoadManifestMissingFile(t *testing.T) {
	if _, err := LoadManifest(filepath.Join(t.TempDir(), "nope.json")); err == nil {
		t.Error("missing manifest: want error")
	}
}

func TestJoinRebuildsByteIdentical(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "orig.bin")
	data := deterministicBytes(1000)
	writeFile(t, src, data)

	parts := filepath.Join(dir, "chunks")
	mustSplit(t, src, 333, parts, SplitOpts{})

	out := filepath.Join(dir, "joined.bin")
	if err := Join(filepath.Join(parts, ManifestFileName), out, JoinOpts{}); err != nil {
		t.Fatalf("Join: %v", err)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(data) {
		t.Fatalf("joined output differs from source (%d vs %d bytes)", len(got), len(data))
	}
}

func TestJoinHashMismatch(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "orig.bin")
	writeFile(t, src, deterministicBytes(1000))

	parts := filepath.Join(dir, "chunks")
	m := mustSplit(t, src, 300, parts, SplitOpts{})

	// Flip a byte in part 3 (keeps its size identical).
	partPath := filepath.Join(parts, m.Parts[2].File)
	b, err := os.ReadFile(partPath)
	if err != nil {
		t.Fatal(err)
	}
	b[0] ^= 0xff
	writeFile(t, partPath, b)

	out := filepath.Join(dir, "joined.bin")
	err = Join(filepath.Join(parts, ManifestFileName), out, JoinOpts{})
	if err == nil {
		t.Fatal("Join with a corrupt part: want error, got nil")
	}
	if !errors.Is(err, ErrHashMismatch) {
		t.Errorf("want errors.Is(err, ErrHashMismatch), got %v", err)
	}
	if !strings.Contains(err.Error(), "part 3") {
		t.Errorf("error should identify part 3, got: %v", err)
	}
	if _, statErr := os.Stat(out); !os.IsNotExist(statErr) {
		t.Errorf("policy: output must not be written on mismatch; stat: %v", statErr)
	}
}

func TestJoinSizeMismatch(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "orig.bin")
	writeFile(t, src, deterministicBytes(1000))

	parts := filepath.Join(dir, "chunks")
	m := mustSplit(t, src, 300, parts, SplitOpts{})

	// Truncate part 2 by one byte.
	partPath := filepath.Join(parts, m.Parts[1].File)
	b, err := os.ReadFile(partPath)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, partPath, b[:len(b)-1])

	out := filepath.Join(dir, "joined.bin")
	err = Join(filepath.Join(parts, ManifestFileName), out, JoinOpts{})
	if err == nil {
		t.Fatal("Join with a truncated part: want error, got nil")
	}
	if !errors.Is(err, ErrHashMismatch) {
		t.Errorf("want errors.Is(err, ErrHashMismatch), got %v", err)
	}
	if !strings.Contains(err.Error(), "part 2") {
		t.Errorf("error should identify part 2, got: %v", err)
	}
	if _, statErr := os.Stat(out); !os.IsNotExist(statErr) {
		t.Errorf("output must not be written on mismatch; stat: %v", statErr)
	}
}

func TestJoinMissingPart(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "orig.bin")
	writeFile(t, src, deterministicBytes(500))

	parts := filepath.Join(dir, "chunks")
	m := mustSplit(t, src, 100, parts, SplitOpts{})

	if err := os.Remove(filepath.Join(parts, m.Parts[1].File)); err != nil {
		t.Fatal(err)
	}

	out := filepath.Join(dir, "joined.bin")
	err := Join(filepath.Join(parts, ManifestFileName), out, JoinOpts{})
	if err == nil {
		t.Fatal("Join with a missing part: want error, got nil")
	}
	if errors.Is(err, ErrHashMismatch) {
		t.Errorf("missing part is not a hash mismatch: %v", err)
	}
	if !strings.Contains(err.Error(), "part 2") {
		t.Errorf("error should identify part 2, got: %v", err)
	}
}

func TestJoinResolvesPartsRelativeToManifest(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "moved.bin")
	data := deterministicBytes(777)
	writeFile(t, src, data)

	parts := filepath.Join(dir, "chunks")
	mustSplit(t, src, 64, parts, SplitOpts{})
	mp := filepath.Join(parts, ManifestFileName)

	// Join using only the manifest path, from an unrelated working file.
	out := filepath.Join(dir, "out", "joined.bin")
	if err := Join(mp, out, JoinOpts{}); err != nil {
		t.Fatalf("Join: %v", err)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(data) {
		t.Fatal("joined output differs from source")
	}
}

func TestSplitNoChecksumsAndJoinSkips(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "nc.bin")
	data := deterministicBytes(512)
	writeFile(t, src, data)

	parts := filepath.Join(dir, "chunks")
	m := mustSplit(t, src, 100, parts, SplitOpts{NoChecksums: true})

	for i, p := range m.Parts {
		if p.SHA256 != strings.Repeat("0", 64) {
			t.Errorf("part %d digest = %q, want zero digest", i+1, p.SHA256)
		}
	}

	// Corrupt a part; Join still succeeds because hash verification is skipped.
	partPath := filepath.Join(parts, m.Parts[2].File)
	b, err := os.ReadFile(partPath)
	if err != nil {
		t.Fatal(err)
	}
	b[0] ^= 0xff
	writeFile(t, partPath, b)

	out := filepath.Join(dir, "joined.bin")
	if err := Join(filepath.Join(parts, ManifestFileName), out, JoinOpts{}); err != nil {
		t.Fatalf("Join with no-checksums manifest should skip hashing: %v", err)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) == string(data) {
		t.Fatal("corrupt part must not reproduce the original bytes")
	}
}

func TestAlgoMD5(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "md5.bin")
	data := deterministicBytes(700)
	writeFile(t, src, data)

	parts := filepath.Join(dir, "chunks")
	m := mustSplit(t, src, 300, parts, SplitOpts{Algo: "md5"})
	if m.Algo != "md5" {
		t.Errorf("manifest algo = %q, want md5", m.Algo)
	}
	for i, p := range m.Parts {
		if len(p.SHA256) != 32 {
			t.Errorf("part %d md5 digest len = %d, want 32", i+1, len(p.SHA256))
		}
	}

	mp := filepath.Join(parts, ManifestFileName)

	out := filepath.Join(dir, "joined.bin")
	if err := Join(mp, out, JoinOpts{}); err != nil {
		t.Fatalf("Join md5 manifest: %v", err)
	}
	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(data) {
		t.Fatal("md5 joined output differs from source")
	}

	// Corrupt a part: md5 verification must catch it.
	b, err := os.ReadFile(filepath.Join(parts, m.Parts[0].File))
	if err != nil {
		t.Fatal(err)
	}
	b[0] ^= 0x01
	writeFile(t, filepath.Join(parts, m.Parts[0].File), b)
	err = Join(mp, filepath.Join(dir, "bad.bin"), JoinOpts{})
	if !errors.Is(err, ErrHashMismatch) {
		t.Fatalf("corrupt part with md5: want ErrHashMismatch, got %v", err)
	}
}

func TestSplitRejectsBadAlgo(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "x.bin")
	writeFile(t, src, deterministicBytes(10))
	if _, err := Split(src, 4, dir, SplitOpts{Algo: "crc32"}); err == nil {
		t.Error("unsupported algo: want error")
	}
}

func TestJoinBadAlgoOverride(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "x.bin")
	writeFile(t, src, deterministicBytes(50))
	parts := filepath.Join(dir, "chunks")
	mustSplit(t, src, 16, parts, SplitOpts{})
	err := Join(filepath.Join(parts, ManifestFileName), filepath.Join(dir, "out.bin"),
		JoinOpts{Algo: "crc32"})
	if err == nil {
		t.Fatal("join with unsupported algo override: want error")
	}
}
