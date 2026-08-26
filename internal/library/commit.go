package library

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/scottzx/mycontext/internal/protocol"
	"github.com/scottzx/mycontext/internal/system"
)

// Commit runs the capture commit sequence (§15.1): stage into
// system/staging/<id>, validate and hash every asset, write an immutable
// manifest, journal "staging", atomically rename into the Library, then
// journal "sealed".
//
// Calling Commit again with a RequestID that already reached "sealed"
// returns that same result (Replayed: true) instead of creating a second
// package. If a previous call staged the package but crashed before
// sealing, this call resumes from wherever the journal says it got to and
// finishes sealing it — the same file-safety validation Verify performs.
func Commit(ctx context.Context, layout system.Layout, journal Journal, clock system.Clock, in CaptureInput) (*CommitResult, error) {
	if clock == nil {
		clock = system.NewClock()
	}
	if in.RequestID == "" {
		return nil, protocol.BadInput("request_id is required")
	}

	existingID, ok, err := journal.FindByRequestID(ctx, in.RequestID)
	if err != nil {
		return nil, err
	}
	if ok {
		return resumeCommit(ctx, layout, journal, clock, existingID, in.RequestID)
	}

	now := clock.Now()
	capturedAt := in.CapturedAt
	if capturedAt.IsZero() {
		capturedAt = now
	}
	storageDate, err := deriveStorageDate(capturedAt, in.Timezone)
	if err != nil {
		return nil, err
	}
	in.CapturedAt = capturedAt

	packageID := system.NewID("cap")

	_, m, _, manifestHash, err := stageFiles(layout, packageID, in, storageDate, now)
	if err != nil {
		return nil, err
	}

	if err := recordStaging(ctx, journal, m, manifestHash, now); err != nil {
		return nil, err
	}

	dst, err := renameToFinal(layout, packageID, storageDate)
	if err != nil {
		return nil, err
	}

	if err := journal.MarkSealed(ctx, packageID, clock.Now()); err != nil {
		return nil, err
	}

	return &CommitResult{PackageID: packageID, StorageDate: storageDate, FinalPath: dst, Manifest: m}, nil
}

// resumeCommit implements idempotent retry: a RequestID already on file
// either finished (return the sealed result) or was interrupted after
// staging (finish sealing it now, the same way Verify would).
func resumeCommit(ctx context.Context, layout system.Layout, journal Journal, clock system.Clock, packageID, requestID string) (*CommitResult, error) {
	rec, found, err := journal.Lookup(ctx, packageID)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, protocol.Integrity("journal inconsistent: request_id %q maps to package %q but no record exists", requestID, packageID)
	}

	switch rec.State {
	case StateSealed:
		dst, err := finalDir(layout.Library(), rec.StorageDate, rec.PackageID)
		if err != nil {
			return nil, err
		}
		m, _, err := readManifest(dst)
		if err != nil {
			return nil, err
		}
		return &CommitResult{PackageID: rec.PackageID, StorageDate: rec.StorageDate, FinalPath: dst, Manifest: m, Replayed: true}, nil

	case StateStaging:
		dst, m, err := completeFromStaging(ctx, layout, journal, rec, clock.Now())
		if err != nil {
			return nil, err
		}
		return &CommitResult{PackageID: rec.PackageID, StorageDate: rec.StorageDate, FinalPath: dst, Manifest: m, Replayed: true}, nil

	default:
		return nil, protocol.Integrity("journal has unknown state %q for package %q", rec.State, rec.PackageID)
	}
}

// stageFiles is §15.1 steps 2-4: copy every input into
// system/staging/<packageID>, validate and hash each asset, then write the
// immutable manifest. It never touches the Journal or the Library tree.
func stageFiles(layout system.Layout, packageID string, in CaptureInput, storageDate string, now time.Time) (stagingDir string, m Manifest, manifestRaw []byte, manifestHash string, err error) {
	if len(in.Files) == 0 {
		return "", Manifest{}, nil, "", protocol.BadInput("capture requires at least one file")
	}

	if err := ensureRealDir(layout.Staging()); err != nil {
		return "", Manifest{}, nil, "", err
	}
	stagingDir = filepath.Join(layout.Staging(), packageID)
	if err := ensureRealDir(stagingDir); err != nil {
		return "", Manifest{}, nil, "", err
	}

	assets := make([]ManifestAsset, 0, len(in.Files))
	for i, f := range in.Files {
		if !validRole(f.Role) {
			return "", Manifest{}, nil, "", protocol.BadInput("unknown asset role %q", f.Role)
		}
		if err := validAssetName(f.Name); err != nil {
			return "", Manifest{}, nil, "", err
		}

		roleDir := filepath.Join(stagingDir, f.Role)
		if err := ensureRealDir(roleDir); err != nil {
			return "", Manifest{}, nil, "", err
		}

		dst := filepath.Join(roleDir, f.Name)
		size, sha, mime, err := copyAsset(f.SourcePath, dst)
		if err != nil {
			return "", Manifest{}, nil, "", err
		}

		assets = append(assets, ManifestAsset{
			RelativePath: filepath.Join(f.Role, f.Name),
			Role:         f.Role,
			SequenceNo:   i + 1,
			SizeBytes:    size,
			SHA256:       sha,
			MimeType:     mime,
		})
	}

	timezone := in.Timezone
	if timezone == "" {
		timezone = DefaultTimezone
	}

	m = Manifest{
		ManifestVersion: ManifestVersion,
		PackageID:       packageID,
		RequestID:       in.RequestID,
		CapturedAt:      in.CapturedAt,
		Timezone:        timezone,
		StorageDate:     storageDate,
		SourceRef:       in.SourceRef,
		CaptureMethod:   in.CaptureMethod,
		Suggested:       in.Suggested,
		Assets:          assets,
	}

	_, manifestRaw, manifestHash, err = writeManifest(stagingDir, m)
	if err != nil {
		return "", Manifest{}, nil, "", err
	}
	return stagingDir, m, manifestRaw, manifestHash, nil
}

// recordStaging is §15.1 step 5.
func recordStaging(ctx context.Context, journal Journal, m Manifest, manifestHash string, now time.Time) error {
	return journal.MarkStaging(ctx, JournalRecord{
		PackageID:    m.PackageID,
		RequestID:    m.RequestID,
		State:        StateStaging,
		StorageDate:  m.StorageDate,
		ManifestHash: manifestHash,
		StagedAt:     now,
	})
}

// renameToFinal is §15.1 step 6: the atomic rename from staging into the
// Library's date-sharded tree. Staging and the Library both live under the
// same Root, so this is a same-filesystem rename; a cross-device Root would
// surface as an ordinary rename error here rather than silently copying.
func renameToFinal(layout system.Layout, packageID, storageDate string) (string, error) {
	stagingDir := filepath.Join(layout.Staging(), packageID)
	dst, err := finalDir(layout.Library(), storageDate, packageID)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return "", protocol.Wrap(err, protocol.CodeIntegrity, "cannot create library date directory")
	}
	if err := os.Rename(stagingDir, dst); err != nil {
		return "", protocol.Wrap(err, protocol.CodeIntegrity, fmt.Sprintf("cannot rename staged package into %q", dst))
	}
	return dst, nil
}

// completeFromStaging finishes an interrupted commit found in the shape of
// §15.2's "staging / staging dir present / final absent" row: validate what
// was staged, then continue the rename and seal it. Both Commit's own retry
// path and Verify's recovery use this so the two never disagree about what
// "safe to continue" means.
func completeFromStaging(ctx context.Context, layout system.Layout, journal Journal, rec JournalRecord, now time.Time) (string, Manifest, error) {
	stagingDir := filepath.Join(layout.Staging(), rec.PackageID)
	m, raw, err := readManifest(stagingDir)
	if err != nil {
		return "", Manifest{}, err
	}
	if err := validateManifestHash(raw, rec.ManifestHash); err != nil {
		return "", Manifest{}, err
	}
	if err := validateAssets(stagingDir, m); err != nil {
		return "", Manifest{}, err
	}
	dst, err := renameToFinal(layout, rec.PackageID, rec.StorageDate)
	if err != nil {
		return "", Manifest{}, err
	}
	if err := journal.MarkSealed(ctx, rec.PackageID, now); err != nil {
		return "", Manifest{}, err
	}
	return dst, m, nil
}

// completeFromFinal finishes an interrupted commit found in the shape of
// §15.2's "staging / staging dir absent / final present" row: the rename
// already happened, only the final journal write is missing. Validate the
// manifest in place, then seal it — no file move needed.
func completeFromFinal(ctx context.Context, journal Journal, rec JournalRecord, finalPath string, now time.Time) (Manifest, error) {
	m, raw, err := readManifest(finalPath)
	if err != nil {
		return Manifest{}, err
	}
	if err := validateManifestHash(raw, rec.ManifestHash); err != nil {
		return Manifest{}, err
	}
	if err := validateAssets(finalPath, m); err != nil {
		return Manifest{}, err
	}
	if err := journal.MarkSealed(ctx, rec.PackageID, now); err != nil {
		return Manifest{}, err
	}
	return m, nil
}
