package ops

import (
	"context"

	"github.com/scottzx/mycontext/internal/library"
	"github.com/scottzx/mycontext/internal/system"
)

// VerifyLibrary reconciles the Library's journal (library_packages) against
// what is actually on disk, implementing every row of the crash-recovery
// matrix at technical design §15.2. It never fabricates a file it cannot
// find and never deletes a user's file - see library.Verify's own doc
// comment for the full behaviour. Both `library verify` and `doctor` call
// this so the two never disagree about the state of the Library.
func (s *Store) VerifyLibrary(ctx context.Context, layout system.Layout) (*library.Report, error) {
	journal := s.NewLibraryJournal()
	return library.Verify(ctx, layout, journal, s.clock)
}
