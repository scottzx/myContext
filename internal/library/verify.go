package library

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/scottzx/mycontext/internal/protocol"
	"github.com/scottzx/mycontext/internal/system"
)

// Action classifies the recovery outcome for one capture package.
type Action string

const (
	// ActionOK means journal and filesystem agree: sealed, present,
	// unmodified. §15.2 row "sealed / 无 staging / 有 final".
	ActionOK Action = "ok"

	// ActionResumedSealed means an interrupted commit was validated and
	// completed here: either the rename ran and the package was sealed
	// (row "staging / 有 staging / 无 final"), or the rename had already
	// happened and only the seal was missing (row "staging / 无 staging /
	// 有 final").
	ActionResumedSealed Action = "resumed_sealed"

	// ActionOrphaned means staged bytes exist with no journal record at
	// all. §15.2 row "无记录 / 有 staging / 无 final". The package is
	// moved to library/_system/orphaned for a human (or a later, more
	// informed pass) to confirm or discard.
	ActionOrphaned Action = "orphaned"

	// ActionPendingAdoption means a sealed-looking package exists on disk
	// with no journal record. §15.2 row "无记录 / 无 staging / 有 final".
	// Nothing is moved: the files are already in their correct sealed
	// location. The manifest is returned so a caller can rebuild the
	// business-level index (that is context.db's job, not this
	// package's).
	ActionPendingAdoption Action = "pending_adoption"

	// ActionIntegrityError means the journal says sealed but neither a
	// staging nor a final copy exists. §15.2 row "sealed / 无 staging /
	// 无 final". This is reported loudly and nothing is touched: recovery
	// must never fabricate a file.
	ActionIntegrityError Action = "integrity_error"

	// ActionQuarantined means a manifest or asset failed validation
	// (corrupt manifest.json, a hash recorded at staging time no longer
	// matching the file, or an asset's bytes not matching its recorded
	// hash). The package is moved to library/_system/quarantine rather
	// than completed or trusted as-is.
	ActionQuarantined Action = "quarantined"

	// ActionUnexpectedState covers any journal/filesystem combination
	// outside the six rows above (for example a journal record whose
	// bytes were already quarantined by a previous run). Nothing is
	// touched or deleted; it is reported for manual review rather than
	// silently ignored.
	ActionUnexpectedState Action = "unexpected_state"
)

// Entry is the recovery outcome for one capture package.
type Entry struct {
	PackageID string
	Action    Action
	Detail    string

	// Path is where the package physically ended up after this pass
	// (final Library path for OK/ResumedSealed/PendingAdoption, the
	// orphaned/quarantine holding path for those actions).
	Path string

	// Manifest is populated when it could be read, so a caller can act on
	// PendingAdoption without a second filesystem pass.
	Manifest *Manifest
}

// Report is the result of one Verify pass.
type Report struct {
	Entries []Entry
}

// Verify reconciles the Journal against the filesystem, implementing every
// row of §15.2's crash-recovery matrix. It never fabricates a file it
// cannot find and never deletes a user's file — the worst it does is move a
// package's folder to library/_system/{orphaned,quarantine}, which stays
// fully recoverable.
func Verify(ctx context.Context, layout system.Layout, journal Journal, clock system.Clock) (*Report, error) {
	if clock == nil {
		clock = system.NewClock()
	}

	stagingIDs, err := scanStagingIDs(layout.Staging())
	if err != nil {
		return nil, err
	}
	finalByID, err := scanFinalPackages(layout.Library())
	if err != nil {
		return nil, err
	}
	journalIDs, err := journal.ListPackageIDs(ctx)
	if err != nil {
		return nil, err
	}

	seen := map[string]struct{}{}
	for id := range stagingIDs {
		seen[id] = struct{}{}
	}
	for id := range finalByID {
		seen[id] = struct{}{}
	}
	for _, id := range journalIDs {
		seen[id] = struct{}{}
	}

	ids := make([]string, 0, len(seen))
	for id := range seen {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	report := &Report{}
	now := clock.Now()
	for _, id := range ids {
		rec, found, err := journal.Lookup(ctx, id)
		if err != nil {
			return nil, err
		}
		_, hasStaging := stagingIDs[id]
		finfo, hasFinal := finalByID[id]

		entry := reconcileOne(ctx, layout, journal, id, rec, found, hasStaging, hasFinal, finfo, now)
		report.Entries = append(report.Entries, entry)
	}
	return report, nil
}

func reconcileOne(ctx context.Context, layout system.Layout, journal Journal, id string, rec JournalRecord, found, hasStaging, hasFinal bool, finfo finalInfo, now time.Time) Entry {
	switch {
	case !found && hasStaging && !hasFinal:
		// §15.2 row 1.
		return reconcileOrphan(layout, id)

	case found && rec.State == StateStaging && hasStaging && !hasFinal:
		// §15.2 row 2.
		return reconcileResumeFromStaging(ctx, layout, journal, rec, now)

	case found && rec.State == StateStaging && !hasStaging && hasFinal:
		// §15.2 row 3.
		return reconcileResumeFromFinal(ctx, layout, journal, rec, finfo.Path, now)

	case found && rec.State == StateSealed && !hasStaging && hasFinal:
		// §15.2 row 4, with a defensive re-validation: a sealed package
		// whose manifest was altered after sealing is exactly the
		// "corrupted manifest hash" case and must not be reported OK.
		return reconcileSealedNormal(layout, rec, finfo.Path)

	case found && rec.State == StateSealed && !hasStaging && !hasFinal:
		// §15.2 row 5.
		return Entry{
			PackageID: id,
			Action:    ActionIntegrityError,
			Detail:    "journal says sealed but neither a staging nor a final copy exists; refusing to fabricate a file",
		}

	case !found && !hasStaging && hasFinal:
		// §15.2 row 6.
		return reconcilePendingAdoption(id, finfo.Path)

	default:
		return Entry{
			PackageID: id,
			Action:    ActionUnexpectedState,
			Detail: fmt.Sprintf(
				"journal_found=%v journal_state=%q has_staging=%v has_final=%v does not match any documented recovery row; left untouched for manual review",
				found, rec.State, hasStaging, hasFinal,
			),
		}
	}
}

func reconcileOrphan(layout system.Layout, id string) Entry {
	stagingDir := filepath.Join(layout.Staging(), id)
	m, _, err := readManifest(stagingDir)
	if err == nil {
		err = validateAssets(stagingDir, m)
	}
	if err != nil && errors.Is(err, ErrManifestInvalid) {
		dst, moveErr := moveToSystemDir(layout, stagingDir, "quarantine", id)
		if moveErr != nil {
			return Entry{PackageID: id, Action: ActionUnexpectedState, Detail: fmt.Sprintf("staged package failed validation (%v) but could not be quarantined: %v", err, moveErr)}
		}
		return Entry{PackageID: id, Action: ActionQuarantined, Detail: err.Error(), Path: dst}
	}
	if err != nil {
		return Entry{PackageID: id, Action: ActionUnexpectedState, Detail: fmt.Sprintf("cannot evaluate orphaned package: %v", err)}
	}

	dst, err := moveToSystemDir(layout, stagingDir, "orphaned", id)
	if err != nil {
		return Entry{PackageID: id, Action: ActionUnexpectedState, Detail: fmt.Sprintf("valid staged package has no journal record but could not be moved to orphaned: %v", err)}
	}
	return Entry{
		PackageID: id,
		Action:    ActionOrphaned,
		Detail:    "staged package has no journal record of any kind; moved aside for confirmation or safe cleanup",
		Path:      dst,
		Manifest:  &m,
	}
}

func reconcileResumeFromStaging(ctx context.Context, layout system.Layout, journal Journal, rec JournalRecord, now time.Time) Entry {
	dst, m, err := completeFromStaging(ctx, layout, journal, rec, now)
	if err != nil {
		if errors.Is(err, ErrManifestInvalid) {
			stagingDir := filepath.Join(layout.Staging(), rec.PackageID)
			qdst, moveErr := moveToSystemDir(layout, stagingDir, "quarantine", rec.PackageID)
			if moveErr != nil {
				return Entry{PackageID: rec.PackageID, Action: ActionUnexpectedState, Detail: fmt.Sprintf("staged package failed validation (%v) but could not be quarantined: %v", err, moveErr)}
			}
			return Entry{PackageID: rec.PackageID, Action: ActionQuarantined, Detail: err.Error(), Path: qdst}
		}
		return Entry{PackageID: rec.PackageID, Action: ActionUnexpectedState, Detail: fmt.Sprintf("cannot complete interrupted commit: %v", err)}
	}
	return Entry{
		PackageID: rec.PackageID,
		Action:    ActionResumedSealed,
		Detail:    "commit was interrupted after staging; validated and completed the rename and seal",
		Path:      dst,
		Manifest:  &m,
	}
}

func reconcileResumeFromFinal(ctx context.Context, layout system.Layout, journal Journal, rec JournalRecord, finalPath string, now time.Time) Entry {
	m, err := completeFromFinal(ctx, journal, rec, finalPath, now)
	if err != nil {
		if errors.Is(err, ErrManifestInvalid) {
			dst, moveErr := moveToSystemDir(layout, finalPath, "quarantine", rec.PackageID)
			if moveErr != nil {
				return Entry{PackageID: rec.PackageID, Action: ActionUnexpectedState, Detail: fmt.Sprintf("sealed-location package failed validation (%v) but could not be quarantined: %v", err, moveErr)}
			}
			return Entry{PackageID: rec.PackageID, Action: ActionQuarantined, Detail: err.Error(), Path: dst}
		}
		return Entry{PackageID: rec.PackageID, Action: ActionUnexpectedState, Detail: fmt.Sprintf("cannot complete interrupted commit: %v", err)}
	}
	return Entry{
		PackageID: rec.PackageID,
		Action:    ActionResumedSealed,
		Detail:    "commit was interrupted after the rename but before the seal was journalled; validated and journalled sealed",
		Path:      finalPath,
		Manifest:  &m,
	}
}

func reconcileSealedNormal(layout system.Layout, rec JournalRecord, finalPath string) Entry {
	m, raw, err := readManifest(finalPath)
	if err == nil {
		if hashErr := validateManifestHash(raw, rec.ManifestHash); hashErr != nil {
			err = hashErr
		} else {
			err = validateAssets(finalPath, m)
		}
	}
	if err != nil && errors.Is(err, ErrManifestInvalid) {
		dst, moveErr := moveToSystemDir(layout, finalPath, "quarantine", rec.PackageID)
		if moveErr != nil {
			return Entry{PackageID: rec.PackageID, Action: ActionUnexpectedState, Detail: fmt.Sprintf("sealed package failed validation (%v) but could not be quarantined: %v", err, moveErr)}
		}
		return Entry{PackageID: rec.PackageID, Action: ActionQuarantined, Detail: err.Error(), Path: dst}
	}
	if err != nil {
		return Entry{PackageID: rec.PackageID, Action: ActionUnexpectedState, Detail: fmt.Sprintf("cannot evaluate sealed package: %v", err)}
	}
	return Entry{PackageID: rec.PackageID, Action: ActionOK, Path: finalPath, Manifest: &m}
}

func reconcilePendingAdoption(id, finalPath string) Entry {
	m, _, err := readManifest(finalPath)
	if err != nil {
		return Entry{
			PackageID: id,
			Action:    ActionPendingAdoption,
			Detail:    fmt.Sprintf("sealed-looking package has no journal record and its manifest could not be read (%v); left in place", err),
			Path:      finalPath,
		}
	}
	return Entry{
		PackageID: id,
		Action:    ActionPendingAdoption,
		Detail:    "sealed-looking package has no journal record; left in place pending index rebuild from its manifest",
		Path:      finalPath,
		Manifest:  &m,
	}
}

// moveToSystemDir relocates a package directory into
// library/_system/{orphaned,quarantine}/<id>. It is a rename, never a
// delete: recovery must never destroy a user's file.
func moveToSystemDir(layout system.Layout, srcDir, category, packageID string) (string, error) {
	destParent := filepath.Join(layout.Library(), "_system", category)
	if err := os.MkdirAll(destParent, 0o700); err != nil {
		return "", protocol.Wrap(err, protocol.CodeIntegrity, fmt.Sprintf("cannot create %s directory", category))
	}
	dst := filepath.Join(destParent, packageID)
	if err := os.Rename(srcDir, dst); err != nil {
		return "", protocol.Wrap(err, protocol.CodeIntegrity, fmt.Sprintf("cannot move %q into %s", srcDir, category))
	}
	return dst, nil
}

// scanStagingIDs lists the top-level package directories currently in
// system/staging.
func scanStagingIDs(stagingRoot string) (map[string]struct{}, error) {
	entries, err := os.ReadDir(stagingRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]struct{}{}, nil
		}
		return nil, protocol.Wrap(err, protocol.CodeIntegrity, "cannot list staging directory")
	}
	ids := map[string]struct{}{}
	for _, e := range entries {
		if e.IsDir() {
			ids[e.Name()] = struct{}{}
		}
	}
	return ids, nil
}

type finalInfo struct {
	Path        string
	StorageDate string
}

// scanFinalPackages walks library/YYYY/MM/DD for capture package
// directories, skipping the reserved _system tree. The layout is a fixed
// three-level date shard (§9.2), so a bounded walk is enough — no need for
// a general recursive tree walk.
func scanFinalPackages(libraryRoot string) (map[string]finalInfo, error) {
	result := map[string]finalInfo{}

	years, err := os.ReadDir(libraryRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return result, nil
		}
		return nil, protocol.Wrap(err, protocol.CodeIntegrity, "cannot list library directory")
	}
	for _, y := range years {
		if !y.IsDir() || y.Name() == "_system" {
			continue
		}
		yearDir := filepath.Join(libraryRoot, y.Name())
		months, err := os.ReadDir(yearDir)
		if err != nil {
			return nil, protocol.Wrap(err, protocol.CodeIntegrity, "cannot list library year directory")
		}
		for _, mo := range months {
			if !mo.IsDir() {
				continue
			}
			monthDir := filepath.Join(yearDir, mo.Name())
			days, err := os.ReadDir(monthDir)
			if err != nil {
				return nil, protocol.Wrap(err, protocol.CodeIntegrity, "cannot list library month directory")
			}
			for _, d := range days {
				if !d.IsDir() {
					continue
				}
				dayDir := filepath.Join(monthDir, d.Name())
				packages, err := os.ReadDir(dayDir)
				if err != nil {
					return nil, protocol.Wrap(err, protocol.CodeIntegrity, "cannot list library day directory")
				}
				storageDate := fmt.Sprintf("%s-%s-%s", y.Name(), mo.Name(), d.Name())
				for _, p := range packages {
					if !p.IsDir() {
						continue
					}
					result[p.Name()] = finalInfo{
						Path:        filepath.Join(dayDir, p.Name()),
						StorageDate: storageDate,
					}
				}
			}
		}
	}
	return result, nil
}
