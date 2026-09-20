// Command fcut splits a binary file into fixed-size parts backed by a JSON
// manifest, and joins the parts back exactly — verifying sizes and checksums
// before the output is finalized. Pure Go standard library, fully offline.
package main

import (
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Conedope/fcut"
)

const version = "1.0.0"

const usageText = `fcut - split and join binary files into fixed-size parts

USAGE:
  fcut split FILE -s SIZE [-d DIR] [--algo sha256|md5] [--no-checksums]
  fcut join MANIFEST -o OUT [--algo sha256|md5]
  fcut --version | --help

COMMANDS:
  split   split FILE into parts of exactly SIZE bytes (last part may be
          shorter) inside DIR. Writes DIR/manifest.json and prints the
          manifest path plus the part count and total bytes.
  join    recombine the parts recorded in MANIFEST into OUT, verifying each
          part's size and checksum before the output is finalized.

OPTIONS:
  split:
    -s, --size SIZE    chunk size; accepts an optional binary suffix
                       K, M, G, T (case-insensitive; 64k = 65536 bytes,
                       1M = 1048576 bytes). Required.
    -d, --dir DIR      destination directory for parts and manifest
                       (default ".").
    -a, --algo NAME    checksum algorithm: sha256 (default) or md5.
        --no-checksums store zero hashes in the manifest; joins then skip
                       hash verification (size checks still run). The output
                       is not verified against the source. Use at your own
                       risk; corruption will go undetected.
  join:
    -o, --output OUT   output file. Required.
    -a, --algo NAME    override the checksum algorithm recorded in the
                       manifest (default: the manifest's algorithm).

EXIT CODES:
  0  success
  1  runtime failure (missing/invalid file, split error, hash or size
     mismatch)
  2  usage error (unknown command, missing or invalid arguments, size < 1)

NOTES:
  A manifest records the source path, total size, chunk size, per-part file
  names/sizes/digests and a sha256-based ("fcut-manifest-1") schema. Part
  files are looked up relative to the manifest's directory on join, so move
  the manifest together with its parts.
`

// valueFlags are CLI flags that consume a following argument in the reorder
// pass, which lets flags appear after the positional file/manifest argument.
var valueFlags = map[string]bool{
	"-s": true, "--size": true, "--chunk-size": true,
	"-d": true, "--dir": true,
	"-o": true, "--output": true,
	"-a": true, "--algo": true,
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run is the CLI entry point split out for testability. It returns the exit
// code.
func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stderr, usageText)
		return 2
	}

	// Global flags recognized anywhere.
	for _, a := range args {
		switch a {
		case "--version", "-version", "-v":
			fmt.Fprintf(stdout, "fcut %s\n", version)
			return 0
		case "--help", "-help", "-h", "help":
			fmt.Fprint(stdout, usageText)
			return 0
		}
	}

	switch args[0] {
	case "split":
		return runSplit(args[1:], stdout, stderr)
	case "join":
		return runJoin(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "fcut: unknown command %q\n\n%s", args[0], usageText)
		return 2
	}
}

func runSplit(args []string, stdout, stderr io.Writer) int {
	flags, pos := reorder(args)
	fs := flag.NewFlagSet("split", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	var sizeStr, dir, algo string
	var noChecksums bool
	fs.StringVar(&sizeStr, "s", "", "chunk size (e.g. 64k, 1M)")
	fs.StringVar(&sizeStr, "size", "", "chunk size (e.g. 64k, 1M)")
	fs.StringVar(&sizeStr, "chunk-size", "", "chunk size (e.g. 64k, 1M)")
	fs.StringVar(&dir, "d", ".", "destination directory")
	fs.StringVar(&dir, "dir", ".", "destination directory")
	fs.StringVar(&algo, "a", "", "checksum algorithm: sha256|md5")
	fs.StringVar(&algo, "algo", "", "checksum algorithm: sha256|md5")
	fs.BoolVar(&noChecksums, "no-checksums", false, "store zero hashes, skip join verification")

	if err := fs.Parse(flags); err != nil {
		fmt.Fprintf(stderr, "fcut split: %v\n\n%s", err, usageText)
		return 2
	}
	if len(pos) != 1 {
		fmt.Fprintf(stderr, "fcut split: expected exactly one FILE argument, got %d\n", len(pos))
		return 2
	}
	if sizeStr == "" {
		fmt.Fprintln(stderr, "fcut split: -s/--size is required")
		return 2
	}
	chunk, err := parseSize(sizeStr)
	if err != nil {
		fmt.Fprintf(stderr, "fcut split: %v\n", err)
		return 2
	}
	if chunk < 1 {
		fmt.Fprintf(stderr, "fcut split: chunk size must be >= 1, got %d\n", chunk)
		return 2
	}

	m, err := fcut.Split(pos[0], int(chunk), dir, fcut.SplitOpts{
		Algo:        algo,
		NoChecksums: noChecksums,
	})
	if err != nil {
		fmt.Fprintf(stderr, "fcut split: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "manifest: %s\n", filepath.Join(dir, fcut.ManifestFileName))
	fmt.Fprintf(stdout, "%d parts, %d bytes\n", len(m.Parts), m.TotalSize)
	return 0
}

func runJoin(args []string, stdout, stderr io.Writer) int {
	flags, pos := reorder(args)
	fs := flag.NewFlagSet("join", flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	var out, algo string
	fs.StringVar(&out, "o", "", "output file")
	fs.StringVar(&out, "output", "", "output file")
	fs.StringVar(&algo, "a", "", "checksum algorithm override")
	fs.StringVar(&algo, "algo", "", "checksum algorithm override")

	if err := fs.Parse(flags); err != nil {
		fmt.Fprintf(stderr, "fcut join: %v\n\n%s", err, usageText)
		return 2
	}
	if len(pos) != 1 {
		fmt.Fprintf(stderr, "fcut join: expected exactly one MANIFEST argument, got %d\n", len(pos))
		return 2
	}
	if out == "" {
		fmt.Fprintln(stderr, "fcut join: -o/--output is required")
		return 2
	}

	if err := fcut.Join(pos[0], out, fcut.JoinOpts{Algo: algo}); err != nil {
		fmt.Fprintf(stderr, "fcut join: %v\n", err)
		return 1
	}
	// Re-read the success message numbers from the (now proven-valid) manifest.
	m, err := fcut.LoadManifest(pos[0])
	if err != nil {
		fmt.Fprintf(stderr, "fcut join: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "joined %d bytes, %d parts verified\n", m.TotalSize, len(m.Parts))
	return 0
}

// reorder separates flag tokens (with their values) from positional arguments
// so flags may be placed before or after the positional FILE/MANIFEST.
func reorder(args []string) (flags, pos []string) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if len(a) > 1 && a[0] == '-' {
			flags = append(flags, a)
			key := a
			if eq := strings.IndexByte(a, '='); eq >= 0 {
				key = a[:eq]
			}
			if valueFlags[key] && !strings.Contains(a, "=") && i+1 < len(args) {
				i++
				flags = append(flags, args[i])
			}
		} else {
			pos = append(pos, a)
		}
	}
	return flags, pos
}

// parseSize parses a byte size with an optional binary suffix: "", B, K/KB/KiB,
// M/MB/MiB, G/GB/GiB, T/TB/TiB. Suffixes are case-insensitive and powers of
// two (64k = 65536, 1M = 1048576). A negative or unparsable size is an error.
func parseSize(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("invalid size %q", s)
	}
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	if i == 0 {
		return 0, fmt.Errorf("invalid size %q (expected a byte count with an optional K/M/G/T suffix)", s)
	}
	n, err := strconv.ParseInt(s[:i], 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid size %q: %v", s, err)
	}

	var mult int64
	switch strings.ToUpper(s[i:]) {
	case "", "B":
		mult = 1
	case "K", "KB", "KIB":
		mult = 1 << 10
	case "M", "MB", "MIB":
		mult = 1 << 20
	case "G", "GB", "GIB":
		mult = 1 << 30
	case "T", "TB", "TIB":
		mult = 1 << 40
	default:
		return 0, fmt.Errorf("invalid size %q: unknown suffix %q (want K/M/G/T, binary)", s, s[i:])
	}
	if n > math.MaxInt64/mult {
		return 0, fmt.Errorf("invalid size %q: too large", s)
	}
	return n * mult, nil
}
