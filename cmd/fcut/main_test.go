package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseSize(t *testing.T) {
	cases := []struct {
		in   string
		want int64
		ok   bool
	}{
		{"512", 512, true},
		{"0", 0, true},
		{"1B", 1, true},
		{"1b", 1, true},
		{"64k", 64 << 10, true},
		{"64K", 64 << 10, true},
		{"1M", 1 << 20, true},
		{"1m", 1 << 20, true},
		{"2G", 2 << 30, true},
		{"1T", 1 << 40, true},
		{"1KB", 1024, true},
		{"1kb", 1024, true},
		{"1MiB", 1 << 20, true},
		{"1mib", 1 << 20, true},
		{"10GB", 10 << 30, true},
		{"", 0, false},
		{"-50", 0, false},
		{"1.5M", 0, false},
		{"abc", 0, false},
		{"1X", 0, false},
		{"12MBjunk", 0, false},
	}
	for _, c := range cases {
		got, err := parseSize(c.in)
		if c.ok && err != nil {
			t.Errorf("parseSize(%q): unexpected error %v", c.in, err)
			continue
		}
		if !c.ok {
			if err == nil {
				t.Errorf("parseSize(%q): want error, got %d", c.in, got)
			}
			continue
		}
		if got != c.want {
			t.Errorf("parseSize(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}

func expectExit(t *testing.T, code int, err error) {
	t.Helper()
	if err == nil && code != 0 {
		t.Fatalf("want exit code %d, got success", code)
	}
	if err == nil {
		return
	}
	var ee *exec.ExitError
	if !asExitError(err, &ee) {
		t.Fatalf("unexpected error type: %v", err)
	}
	if ee.ExitCode() != code {
		t.Fatalf("exit code = %d, want %d (stderr: %v)", ee.ExitCode(), code, readStderr(ee))
	}
}

func asExitError(err error, ee **exec.ExitError) bool {
	if err == nil {
		return false
	}
	e, ok := err.(*exec.ExitError)
	if !ok {
		return false
	}
	*ee = e
	return true
}

func readStderr(ee *exec.ExitError) string {
	return strings.TrimSpace(string(ee.Stderr))
}

func stdoutOf(t *testing.T, ee *exec.ExitError) string {
	t.Helper()
	return string(ee.Stderr) // combined output when both set
}

// deterministicBytes provides content that is unlikely to have long identical
// runs, so corruption tests are meaningful.
func deterministicBytes(n int) []byte {
	b := make([]byte, n)
	for i := range b {
		b[i] = byte((i*31 + i/7 + 5) % 256)
	}
	return b
}

var binPath string

func TestMain(m *testing.M) {
	tmp, err := os.MkdirTemp("", "fcut-cli-test-*")
	if err != nil {
		panic(err)
	}
	binPath = filepath.Join(tmp, "fcut-test-bin")
	cmd := exec.Command("go", "build", "-o", binPath, ".")
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		os.RemoveAll(tmp)
		panic("build fcut binary for tests: " + err.Error() + "\n" + string(out))
	}
	code := m.Run()
	os.RemoveAll(tmp)
	os.Exit(code)
}

func runBin(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(binPath, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func TestRunVersionAndHelp(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"--version"}, &out, &errOut); code != 0 {
		t.Fatalf("--version exit code = %d", code)
	}
	if out.String() != "fcut 1.0.0\n" {
		t.Errorf("--version output = %q", out.String())
	}

	out.Reset()
	if code := run([]string{"--help"}, &out, &errOut); code != 0 {
		t.Fatalf("--help exit code = %d", code)
	}
	if !strings.Contains(out.String(), "USAGE") {
		t.Errorf("--help output missing usage:\n%s", out.String())
	}

	out.Reset()
	if code := run(nil, &out, &errOut); code != 2 {
		t.Fatalf("no args exit code = %d, want 2", code)
	}
	out.Reset()
	if code := run([]string{"bogus"}, &out, &errOut); code != 2 {
		t.Fatalf("unknown command exit code = %d, want 2", code)
	}
}

func TestRunSplitUsageErrors(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"split"}, &out, &errOut); code != 2 {
		t.Errorf("split without file: exit %d, want 2", code)
	}
	errOut.Reset()
	if code := run([]string{"split", "file.bin"}, &out, &errOut); code != 2 {
		t.Errorf("split without -s: exit %d, want 2", code)
	}
	errOut.Reset()
	if code := run([]string{"split", "file.bin", "-s", "0"}, &out, &errOut); code != 2 {
		t.Errorf("split -s 0: exit %d, want 2", code)
	}
	errOut.Reset()
	if code := run([]string{"split", "file.bin", "-s", "banana"}, &out, &errOut); code != 2 {
		t.Errorf("split -s banana: exit %d, want 2", code)
	}
	errOut.Reset()
	if code := run([]string{"split", "file.bin", "-s", "4", "extra"}, &out, &errOut); code != 2 {
		t.Errorf("split with extra positional: exit %d, want 2", code)
	}
	errOut.Reset()
	if code := run([]string{"join", "m.json"}, &out, &errOut); code != 2 {
		t.Errorf("join without -o: exit %d, want 2", code)
	}
}

func TestRunSplitMissingSource(t *testing.T) {
	dir := t.TempDir()
	var out, errOut bytes.Buffer
	code := run([]string{"split", filepath.Join(dir, "nope.bin"), "-s", "10"}, &out, &errOut)
	if code != 1 {
		t.Errorf("split of missing file: exit %d, want 1", code)
	}
	if !strings.Contains(errOut.String(), "nope.bin") {
		t.Errorf("stderr should name missing file: %q", errOut.String())
	}
}

// --- End-to-end tests against the built binary. ---

func TestCLISplitJoinRoundTrip(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "sample.bin")
	if err := os.WriteFile(src, deterministicBytes(5000), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := runBin(t, dir, "split", "sample.bin", "-s", "256", "-d", "chunks")
	expectExit(t, 0, err)
	if !strings.Contains(out, "manifest: chunks/manifest.json") {
		t.Errorf("split stdout missing manifest path:\n%s", out)
	}
	if !strings.Contains(out, "20 parts, 5000 bytes") {
		t.Errorf("split stdout missing part summary:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(dir, "chunks", "manifest.json")); err != nil {
		t.Fatalf("manifest not written: %v", err)
	}

	restored := filepath.Join(dir, "restored.bin")
	out, err = runBin(t, dir, "join", "chunks/manifest.json", "-o", "restored.bin")
	expectExit(t, 0, err)
	if !strings.Contains(out, "joined 5000 bytes, 20 parts verified") {
		t.Errorf("join stdout missing summary:\n%s", out)
	}

	orig, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(restored)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(orig, got) {
		t.Fatal("restored file differs from source")
	}
}

func TestCLISizeSuffixParsing(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "small.bin")
	if err := os.WriteFile(src, deterministicBytes(5000), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := runBin(t, dir, "split", "small.bin", "-s", "1M", "-d", "chunks")
	expectExit(t, 0, err)
	if !strings.Contains(out, "1 parts, 5000 bytes") {
		t.Errorf("-s 1M should parse to 1048576 bytes (1 part for 5000 bytes):\n%s", out)
	}

	out, err = runBin(t, dir, "split", "small.bin", "-s", "64k", "-d", "chunks2")
	expectExit(t, 0, err)
	if !strings.Contains(out, "1 parts, 5000 bytes") {
		t.Errorf("-s 64k should parse to 65536 bytes:\n%s", out)
	}

	out, err = runBin(t, dir, "split", "small.bin", "-s", "0", "-d", "chunks3")
	expectExit(t, 2, err)
}

func TestCLISplitJoinCorruptPart(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "corrupt.bin")
	if err := os.WriteFile(src, deterministicBytes(1000), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := runBin(t, dir, "split", "corrupt.bin", "-s", "128", "-d", "chunks"); err != nil {
		t.Fatalf("split: %v", err)
	}

	// Corrupt part 4.
	part := filepath.Join(dir, "chunks", "corrupt00004.part")
	b, err := os.ReadFile(part)
	if err != nil {
		t.Fatal(err)
	}
	b[0] ^= 0xff
	if err := os.WriteFile(part, b, 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := runBin(t, dir, "join", "chunks/manifest.json", "-o", "restored.bin")
	expectExit(t, 1, err)
	if !strings.Contains(out, "part 4") {
		t.Errorf("join error should identify part 4:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(dir, "restored.bin")); !os.IsNotExist(err) {
		t.Errorf("policy: output must not exist after failed join, stat: %v", err)
	}
}

func TestCLIJoinMissingManifest(t *testing.T) {
	dir := t.TempDir()
	out, err := runBin(t, dir, "join", "does-not-exist.json", "-o", "out.bin")
	expectExit(t, 1, err)
	if !strings.Contains(out, "does-not-exist.json") {
		t.Errorf("stderr should name the missing manifest:\n%s", out)
	}
}

func TestCLIJoinMissingPart(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "mp.bin")
	if err := os.WriteFile(src, deterministicBytes(400), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := runBin(t, dir, "split", "mp.bin", "-s", "64", "-d", "chunks"); err != nil {
		t.Fatalf("split: %v", err)
	}
	if err := os.Remove(filepath.Join(dir, "chunks", "mp00002.part")); err != nil {
		t.Fatal(err)
	}

	out, err := runBin(t, dir, "join", "chunks/manifest.json", "-o", "out.bin")
	expectExit(t, 1, err)
	if !strings.Contains(out, "part 2") {
		t.Errorf("join error should identify part 2:\n%s", out)
	}
}

func TestCLIAlgoMD5RoundTrip(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "m.bin")
	if err := os.WriteFile(src, deterministicBytes(600), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := runBin(t, dir, "split", "m.bin", "-s", "200", "-d", "chunks", "--algo", "md5")
	expectExit(t, 0, err)
	if !strings.Contains(out, "3 parts, 600 bytes") {
		t.Errorf("md5 split summary:\n%s", out)
	}
	out, err = runBin(t, dir, "join", "chunks/manifest.json", "-o", "m.restored.bin")
	expectExit(t, 0, err)
	orig, _ := os.ReadFile(src)
	got, _ := os.ReadFile(filepath.Join(dir, "m.restored.bin"))
	if !bytes.Equal(orig, got) {
		t.Fatal("md5 round-trip differs from source")
	}
}

func TestCLINoChecksums(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "nc.bin")
	if err := os.WriteFile(src, deterministicBytes(400), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := runBin(t, dir, "split", "nc.bin", "-s", "64", "-d", "chunks", "--no-checksums")
	expectExit(t, 0, err)
	if !strings.Contains(out, "7 parts, 400 bytes") {
		t.Errorf("no-checksums split summary:\n%s", out)
	}

	// Corrupt a part: join must still succeed because hashing is skipped.
	part := filepath.Join(dir, "chunks", "nc00003.part")
	b, err := os.ReadFile(part)
	if err != nil {
		t.Fatal(err)
	}
	b[0] ^= 0xff
	if err := os.WriteFile(part, b, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = runBin(t, dir, "join", "chunks/manifest.json", "-o", "nc.restored.bin")
	expectExit(t, 0, err)
}

func TestCLIVersionAndHelp(t *testing.T) {
	dir := t.TempDir()
	out, err := runBin(t, dir, "--version")
	expectExit(t, 0, err)
	if !strings.HasPrefix(out, "fcut 1.0.0") {
		t.Errorf("version output = %q", out)
	}
	out, err = runBin(t, dir, "help")
	expectExit(t, 0, err)
	if !strings.Contains(out, "USAGE") {
		t.Errorf("help output missing usage:\n%s", out)
	}
}
