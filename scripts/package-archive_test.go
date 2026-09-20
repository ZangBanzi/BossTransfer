package main

import (
	"archive/tar"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestWriteArchiveNormalizesModes(t *testing.T) {
	parent := t.TempDir()
	rootName := "release"
	root := filepath.Join(parent, rootName)
	if err := os.MkdirAll(filepath.Join(root, "bin"), 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "bin", "client"), []byte("binary"), 0o666); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("readme"), 0o666); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(parent, "release.tar.gz")
	if err := writeArchive(root, rootName, archive, map[string]bool{"bin/client": true}); err != nil {
		t.Fatal(err)
	}

	file, err := os.Open(archive)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	defer gzipReader.Close()
	reader := tar.NewReader(gzipReader)
	modes := map[string]int64{}
	for {
		header, err := reader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		modes[header.Name] = header.Mode
	}
	if modes["release/"] != 0o755 || modes["release/bin/"] != 0o755 {
		t.Fatalf("directory modes = %#o / %#o", modes["release/"], modes["release/bin/"])
	}
	if modes["release/bin/client"] != 0o755 {
		t.Fatalf("client mode = %#o", modes["release/bin/client"])
	}
	if modes["release/README.md"] != 0o644 {
		t.Fatalf("README mode = %#o", modes["release/README.md"])
	}
}
