package cluster

import (
	"archive/tar"
	"bytes"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/klauspost/compress/zstd"
)

// CreateInitialSync creates a zstd-compressed tar of all synced directories.
// Returns the compressed bytes.
func (s *FileSyncService) CreateInitialSync() ([]byte, error) {
	var buf bytes.Buffer
	zw, err := zstd.NewWriter(&buf)
	if err != nil {
		return nil, fmt.Errorf("creating zstd writer: %w", err)
	}
	tw := tar.NewWriter(zw)

	for _, dir := range s.syncDirs {
		absDir := filepath.Join(s.rootDir, dir)
		if _, err := os.Stat(absDir); os.IsNotExist(err) {
			continue
		}
		err := filepath.WalkDir(absDir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				s.logger.Warn("initial sync: walk error", "path", path, "error", err)
				return nil // skip inaccessible paths
			}
			relPath, relErr := filepath.Rel(s.rootDir, path)
			if relErr != nil {
				return nil
			}
			if shouldIgnore(relPath) {
				if d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			info, err := d.Info()
			if err != nil {
				s.logger.Warn("initial sync: stat error", "path", relPath, "error", err)
				return nil
			}

			header, err := tar.FileInfoHeader(info, "")
			if err != nil {
				s.logger.Warn("initial sync: header error", "path", relPath, "error", err)
				return nil
			}
			header.Name = relPath

			if d.IsDir() {
				header.Name += "/"
				return tw.WriteHeader(header)
			}

			if !info.Mode().IsRegular() {
				return nil
			}
			if info.Size() > maxFileSize {
				s.logger.Warn("skipping large file in initial sync", "path", relPath, "size", info.Size())
				return nil
			}

			if err := tw.WriteHeader(header); err != nil {
				return err
			}
			f, err := os.Open(path)
			if err != nil {
				return nil
			}
			defer func() { _ = f.Close() }()
			_, err = io.Copy(tw, f)
			return err
		})
		if err != nil {
			return nil, fmt.Errorf("walking %s: %w", dir, err)
		}
	}

	if err := tw.Close(); err != nil {
		return nil, fmt.Errorf("closing tar: %w", err)
	}
	if err := zw.Close(); err != nil {
		return nil, fmt.Errorf("closing zstd: %w", err)
	}
	return buf.Bytes(), nil
}

// ApplyInitialSyncStream extracts a zstd-compressed tar from a streaming
// reader. This avoids buffering the entire archive in memory — extraction
// happens on the fly as chunks arrive over the network.
func (s *FileSyncService) ApplyInitialSyncStream(r io.Reader) error {
	zr, err := zstd.NewReader(r)
	if err != nil {
		return fmt.Errorf("opening zstd: %w", err)
	}
	defer zr.Close()

	tr := tar.NewReader(zr)
	for {
		header, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("reading tar: %w", err)
		}

		target := filepath.Join(s.rootDir, header.Name)

		// Prevent path traversal.
		rel, relErr := filepath.Rel(s.rootDir, filepath.Clean(target))
		if relErr != nil || strings.HasPrefix(rel, "..") {
			continue
		}

		// Suppress echo — derive relPath from resolved target rather than
		// trusting header.Name, so echo suppression works regardless of how
		// the tar was produced.
		relPath, _ := filepath.Rel(s.rootDir, target)
		s.suppressEcho(relPath)

		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, os.FileMode(header.Mode)); err != nil {
				return fmt.Errorf("creating dir %s: %w", header.Name, err)
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			if err := atomicWriteFromReader(target, tr, os.FileMode(header.Mode)&0o666); err != nil {
				return fmt.Errorf("writing file %s: %w", header.Name, err)
			}
		}
	}

	s.logger.Info("initial sync applied", "root", s.rootDir)
	return nil
}
