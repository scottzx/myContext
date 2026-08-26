package library

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"github.com/scottzx/mycontext/internal/protocol"
	"github.com/scottzx/mycontext/internal/system"
)

// ErrManifestInvalid marks a manifest or asset failing validation: the
// manifest.json is unreadable/malformed, its recorded hash no longer
// matches the file on disk, or an asset's bytes no longer match what the
// manifest claims. Callers (Verify) use errors.Is to route this into
// quarantine rather than treating it as a generic I/O failure.
var ErrManifestInvalid = errors.New("library: manifest failed validation")

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// writeManifest marshals and writes manifest.json into dir via the same
// temp-file-fsync-rename primitive config.json uses, satisfying §15.1 step
// 4 ("写不可变 manifest 临时文件并 fsync").
func writeManifest(dir string, m Manifest) (path string, raw []byte, hash string, err error) {
	raw, err = json.MarshalIndent(m, "", "  ")
	if err != nil {
		return "", nil, "", protocol.Wrap(err, protocol.CodeInternal, "cannot encode manifest")
	}
	path = filepath.Join(dir, "manifest.json")
	if err := system.WriteFileAtomic(path, raw, 0o600); err != nil {
		return "", nil, "", err
	}
	return path, raw, sha256Hex(raw), nil
}

// readManifest loads and parses manifest.json from a package directory
// (staging or sealed).
func readManifest(dir string) (Manifest, []byte, error) {
	path := filepath.Join(dir, "manifest.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		return Manifest{}, nil, fmt.Errorf("%w: cannot read manifest.json: %v", ErrManifestInvalid, err)
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return Manifest{}, nil, fmt.Errorf("%w: manifest.json is not valid JSON: %v", ErrManifestInvalid, err)
	}
	return m, raw, nil
}

// validateManifestHash confirms manifest.json on disk still matches the
// hash recorded in the journal at MarkStaging time, catching a manifest
// that was altered (or corrupted) after staging.
func validateManifestHash(raw []byte, expectedHash string) error {
	if expectedHash == "" {
		return nil
	}
	got := sha256Hex(raw)
	if got != expectedHash {
		return fmt.Errorf("%w: manifest hash mismatch (expected %s, got %s)", ErrManifestInvalid, expectedHash, got)
	}
	return nil
}

// validateAssets recomputes every asset's size and sha256 against what the
// manifest claims. This is a self-consistency check: it does not require a
// journal record, so it also covers the "无记录" recovery rows where there
// is no external baseline to compare against, only the manifest's own
// claims about the bytes sitting next to it.
func validateAssets(dir string, m Manifest) error {
	for _, a := range m.Assets {
		full := filepath.Join(dir, a.RelativePath)
		info, err := os.Stat(full)
		if err != nil {
			return fmt.Errorf("%w: asset %q missing: %v", ErrManifestInvalid, a.RelativePath, err)
		}
		if info.Size() != a.SizeBytes {
			return fmt.Errorf("%w: asset %q size mismatch (expected %d, got %d)", ErrManifestInvalid, a.RelativePath, a.SizeBytes, info.Size())
		}
		sum, err := sha256File(full)
		if err != nil {
			return fmt.Errorf("%w: cannot hash asset %q: %v", ErrManifestInvalid, a.RelativePath, err)
		}
		if sum != a.SHA256 {
			return fmt.Errorf("%w: asset %q hash mismatch", ErrManifestInvalid, a.RelativePath)
		}
	}
	return nil
}

// copyAsset copies src into dst, refusing to overwrite anything already
// there (O_EXCL — this also refuses a dangling or live symlink planted at
// dst, satisfying §15.3's symlink-escape rule for the destination leaf
// without needing a separate check), and returns the copied size, sha256
// and sniffed MIME type. dst's parent directory must already exist and have
// been verified real by the caller (ensureRealDir).
func copyAsset(src, dst string) (size int64, sha string, mime string, err error) {
	in, err := os.Open(src)
	if err != nil {
		return 0, "", "", protocol.BadInput("cannot open source %q: %v", src, err)
	}
	defer in.Close()

	info, err := in.Stat()
	if err != nil {
		return 0, "", "", protocol.BadInput("cannot stat source %q: %v", src, err)
	}
	if info.IsDir() {
		return 0, "", "", protocol.BadInput("source %q is a directory, not a file", src)
	}

	sniff := make([]byte, 512)
	n, _ := io.ReadFull(in, sniff)
	sniff = sniff[:n]
	mime = http.DetectContentType(sniff)
	reader := io.MultiReader(bytes.NewReader(sniff), in)

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, "", "", protocol.Wrap(err, protocol.CodeIntegrity, fmt.Sprintf("cannot create %q", dst))
	}
	defer out.Close()

	h := sha256.New()
	written, err := io.Copy(io.MultiWriter(out, h), reader)
	if err != nil {
		return 0, "", "", protocol.Wrap(err, protocol.CodeIntegrity, fmt.Sprintf("cannot copy into %q", dst))
	}
	if err := out.Sync(); err != nil {
		return 0, "", "", protocol.Wrap(err, protocol.CodeIntegrity, fmt.Sprintf("cannot fsync %q", dst))
	}
	return written, hex.EncodeToString(h.Sum(nil)), mime, nil
}
