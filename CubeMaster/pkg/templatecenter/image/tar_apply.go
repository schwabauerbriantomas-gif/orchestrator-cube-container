// SPDX-License-Identifier: Apache-2.0
//
// Package image provides OCI image layer extraction without the containerd
// dependency. This replaces github.com/containerd/containerd/archive.Apply
// and github.com/containerd/containerd/archive/compression.DecompressStream
// with a minimal stdlib-based implementation that supports:
//   - gzip and zstd decompression
//   - OCI whiteout files (.wh.*)
//   - OCI opaque directory markers (.wh..wh..opq)
//   - symlinks, hardlinks, directories, regular files
//   - file permissions and ownership
//
// This eliminates the containerd module dependency and its associated CVEs
// (GO-2026-5622, GO-2026-5338, GO-2026-5064) which affect the CRI checkpoint
// subsystem that CubeMaster never uses.

package image

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/klauspost/compress/zstd"
)

// bufioMaybe returns r as-is. Containerd's DecompressStream used a bufio.Reader
// internally; our version peeks the header manually and uses io.MultiReader to
// restore the consumed bytes. No buffering needed for correct operation.
func bufioMaybe(r io.Reader) io.Reader {
	return r
}

// bytesReader wraps a byte slice as an io.Reader.
func bytesReader(b []byte) io.Reader {
	return bytes.NewReader(b)
}

// whiteoutPrefix is the prefix for whiteout files in OCI layers.
const whiteoutPrefix = ".wh."

// opaqueWhiteoutMarker is the filename for opaque directory markers.
// Per OCI spec: a file named ".wh..wh..opq" in a directory signals that
// ALL existing contents of that directory from lower layers should be removed.
const opaqueWhiteoutMarker = whiteoutPrefix + ".wh..opq"

// decompressStream detects the compression algorithm and returns a decompressing reader.
// Supports: gzip, zstd, and uncompressed streams.
func decompressStream(r io.Reader) (io.ReadCloser, error) {
	br := bufioMaybe(r)

	// Peek at the first few bytes to detect compression
	header := make([]byte, 4)
	n, err := io.ReadFull(br, header)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) {
		return nil, fmt.Errorf("failed to read compression header: %w", err)
	}

	// Restore the header bytes by prepending them
	combined := io.MultiReader(bytesReader(header[:n]), br)

	// gzip magic: 0x1f 0x8b
	if n >= 2 && header[0] == 0x1f && header[1] == 0x8b {
		gz, err := gzip.NewReader(combined)
		if err != nil {
			return nil, fmt.Errorf("failed to create gzip reader: %w", err)
		}
		return gz, nil
	}

	// zstd magic: 0x28 0xb5 0x2f 0xfd
	if n >= 4 && header[0] == 0x28 && header[1] == 0xb5 && header[2] == 0x2f && header[3] == 0xfd {
		zr, err := zstd.NewReader(combined)
		if err != nil {
			return nil, fmt.Errorf("failed to create zstd reader: %w", err)
		}
		return zr.IOReadCloser(), nil
	}

	// Uncompressed
	return io.NopCloser(combined), nil
}

// applyTar extracts a tar stream into destDir, handling OCI whiteouts.
// It returns the number of bytes extracted from the raw tar stream.
func applyTar(ctx context.Context, destDir string, r io.Reader) (int64, error) {
	tr := tar.NewReader(r)

	for {
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		default:
		}

		hdr, err := tr.Next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return 0, nil
			}
			return 0, fmt.Errorf("tar read error: %w", err)
		}

		// Sanitize path — prevent path traversal
		cleanName := filepath.Clean(hdr.Name)
		if strings.HasPrefix(cleanName, "..") || filepath.IsAbs(cleanName) {
			return 0, fmt.Errorf("tar entry %q escapes destination directory", hdr.Name)
		}

		targetPath := filepath.Join(destDir, cleanName)

		// Handle whiteout files
		baseName := filepath.Base(cleanName)
		dirName := filepath.Dir(cleanName)

		if baseName == opaqueWhiteoutMarker {
			// Opaque directory marker: remove all existing contents of the directory
			opaqueDir := filepath.Join(destDir, dirName)
			if err := removeDirContents(opaqueDir); err != nil {
				return 0, fmt.Errorf("failed to apply opaque whiteout in %q: %w", dirName, err)
			}
			continue
		}

		if strings.HasPrefix(baseName, whiteoutPrefix) {
			// Whiteout file: remove the corresponding file/directory
			originalName := strings.TrimPrefix(baseName, whiteoutPrefix)
			whiteoutTarget := filepath.Join(destDir, dirName, originalName)
			_ = os.RemoveAll(whiteoutTarget)
			continue
		}

		// Create parent directories
		if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
			return 0, fmt.Errorf("failed to create parent directory for %q: %w", hdr.Name, err)
		}

		if err := extractTarEntry(hdr, targetPath, tr); err != nil {
			return 0, fmt.Errorf("failed to extract %q: %w", hdr.Name, err)
		}
	}
}

// extractTarEntry extracts a single tar header+content to the filesystem.
func extractTarEntry(hdr *tar.Header, targetPath string, r io.Reader) error {
	mode := os.FileMode(hdr.Mode)

	switch hdr.Typeflag {
	case tar.TypeDir:
		return os.MkdirAll(targetPath, mode)

	case tar.TypeReg, tar.TypeRegA:
		f, err := os.OpenFile(targetPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
		if err != nil {
			return err
		}
		defer f.Close()
		if _, err := io.CopyN(f, r, hdr.Size); err != nil {
			return err
		}
		// Ownership applied best-effort (may fail if not root)
		applyOwnership(targetPath, hdr)
		return nil

	case tar.TypeSymlink:
		_ = os.Remove(targetPath)
		if err := os.Symlink(hdr.Linkname, targetPath); err != nil {
			return err
		}
		applyOwnership(targetPath, hdr)
		return nil

	case tar.TypeLink:
		_ = os.Remove(targetPath)
		linkTarget := filepath.Join(filepath.Dir(targetPath), hdr.Linkname)
		if err := os.Link(linkTarget, targetPath); err != nil {
			return err
		}
		return nil

	case tar.TypeFifo:
		_ = os.Remove(targetPath)
		if err := syscall.Mkfifo(targetPath, uint32(mode)); err != nil {
			return err
		}
		applyOwnership(targetPath, hdr)
		return nil

	case tar.TypeChar:
		_ = os.Remove(targetPath)
		if err := syscall.Mknod(targetPath, uint32(mode)|syscall.S_IFCHR, int(mkdev(hdr.Devmajor, hdr.Devminor))); err != nil {
			return err
		}
		applyOwnership(targetPath, hdr)
		return nil

	case tar.TypeBlock:
		_ = os.Remove(targetPath)
		if err := syscall.Mknod(targetPath, uint32(mode)|syscall.S_IFBLK, int(mkdev(hdr.Devmajor, hdr.Devminor))); err != nil {
			return err
		}
		applyOwnership(targetPath, hdr)
		return nil

	default:
		// Skip unknown entry types
		return nil
	}
}

// applyOwnership sets uid/gid from the tar header (best-effort).
func applyOwnership(path string, hdr *tar.Header) {
	if hdr.Uid >= 0 && hdr.Gid >= 0 {
		_ = os.Chown(path, hdr.Uid, hdr.Gid)
	}
}

// removeDirContents removes all files and subdirectories within dir, but keeps dir itself.
func removeDirContents(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, entry := range entries {
		if err := os.RemoveAll(filepath.Join(dir, entry.Name())); err != nil {
			return err
		}
	}
	return nil
}

// mkdev creates a device number from major and minor.
func mkdev(major, minor int64) uint32 {
	return uint32(((major & 0xfff) << 8) | (minor & 0xff))
}
