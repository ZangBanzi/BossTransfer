package main

import (
	"archive/tar"
	"compress/gzip"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func main() {
	source := flag.String("source", "", "directory containing the archive root")
	root := flag.String("root", "", "archive root directory name")
	output := flag.String("output", "", "output .tar.gz path")
	executableList := flag.String("executable", "", "comma-separated paths beneath the archive root")
	flag.Parse()
	if *source == "" || *root == "" || *output == "" {
		fatalf("source, root, and output are required")
	}

	rootPath := filepath.Join(*source, *root)
	rootInfo, err := os.Stat(rootPath)
	if err != nil || !rootInfo.IsDir() {
		fatalf("archive root is not a directory: %s", rootPath)
	}
	executables := map[string]bool{}
	for _, name := range strings.Split(*executableList, ",") {
		name = filepath.ToSlash(strings.TrimSpace(name))
		if name != "" {
			executables[name] = true
		}
	}

	if err := writeArchive(rootPath, *root, *output, executables); err != nil {
		fatalf("package archive: %v", err)
	}
}

func writeArchive(rootPath, rootName, output string, executables map[string]bool) (returnErr error) {
	entries := []string{}
	err := filepath.WalkDir(rootPath, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("symbolic links are not allowed: %s", path)
		}
		entries = append(entries, path)
		return nil
	})
	if err != nil {
		return err
	}
	sort.Strings(entries)

	file, err := os.Create(output)
	if err != nil {
		return err
	}
	defer func() {
		if err := file.Close(); returnErr == nil && err != nil {
			returnErr = err
		}
	}()
	gzipWriter := gzip.NewWriter(file)
	gzipWriter.Header.ModTime = time.Unix(0, 0).UTC()
	gzipWriter.Header.OS = 255
	defer func() {
		if err := gzipWriter.Close(); returnErr == nil && err != nil {
			returnErr = err
		}
	}()
	tarWriter := tar.NewWriter(gzipWriter)
	defer func() {
		if err := tarWriter.Close(); returnErr == nil && err != nil {
			returnErr = err
		}
	}()

	epoch := time.Unix(0, 0).UTC()
	for _, path := range entries {
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(rootPath, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		name := rootName
		if rel != "." {
			name += "/" + rel
		}
		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		header.Name = name
		header.Uid, header.Gid = 0, 0
		header.Uname, header.Gname = "root", "root"
		header.ModTime, header.AccessTime, header.ChangeTime = epoch, epoch, epoch
		header.Format = tar.FormatPAX
		if info.IsDir() {
			header.Mode = 0o755
			header.Name += "/"
		} else if info.Mode().IsRegular() {
			header.Mode = 0o644
			if executables[rel] {
				header.Mode = 0o755
			}
		} else {
			return fmt.Errorf("unsupported file type: %s", path)
		}
		if err := tarWriter.WriteHeader(header); err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			continue
		}
		input, err := os.Open(path)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(tarWriter, input)
		closeErr := input.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
