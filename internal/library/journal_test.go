package library_test

import (
	"context"
	"sync"
	"time"

	"github.com/scottzx/mycontext/internal/library"
)

// fakeJournal is an in-memory Journal used by every test in this package. It
// exists so file-transaction logic can be exercised without the ops/context
// schema, per the design constraint that this package never depends on SQL.
type fakeJournal struct {
	mu              sync.Mutex
	byPackage       map[string]library.JournalRecord
	byRequest       map[string]string
	failMarkStaging bool
	failMarkSealed  bool
}

func newFakeJournal() *fakeJournal {
	return &fakeJournal{
		byPackage: map[string]library.JournalRecord{},
		byRequest: map[string]string{},
	}
}

func (f *fakeJournal) MarkStaging(ctx context.Context, rec library.JournalRecord) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failMarkStaging {
		return errInjectedFault
	}
	f.byPackage[rec.PackageID] = rec
	f.byRequest[rec.RequestID] = rec.PackageID
	return nil
}

func (f *fakeJournal) MarkSealed(ctx context.Context, packageID string, sealedAt time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failMarkSealed {
		return errInjectedFault
	}
	rec, ok := f.byPackage[packageID]
	if !ok {
		return errNoSuchPackage
	}
	rec.State = library.StateSealed
	rec.SealedAt = sealedAt
	f.byPackage[packageID] = rec
	return nil
}

func (f *fakeJournal) Lookup(ctx context.Context, packageID string) (library.JournalRecord, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	rec, ok := f.byPackage[packageID]
	return rec, ok, nil
}

func (f *fakeJournal) FindByRequestID(ctx context.Context, requestID string) (string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	id, ok := f.byRequest[requestID]
	return id, ok, nil
}

func (f *fakeJournal) ListPackageIDs(ctx context.Context) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ids := make([]string, 0, len(f.byPackage))
	for id := range f.byPackage {
		ids = append(ids, id)
	}
	return ids, nil
}

// forget removes a package's journal record entirely, simulating a
// context.db that was restored from a backup taken before this package was
// journalled (used to construct the §15.2 "无记录 / 有 final" row).
func (f *fakeJournal) forget(packageID, requestID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.byPackage, packageID)
	delete(f.byRequest, requestID)
}
