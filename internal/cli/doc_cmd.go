package cli

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/scottzx/mycontext/internal/ops"
)

// newDocCmd exposes the document layer: the metadata rows, their links to
// business objects, and full-text search over their bodies. The bytes
// themselves live in the library as files - a document row stores no text.
func newDocCmd(opts *GlobalOptions) *cobra.Command {
	cmd := &cobra.Command{Use: "doc", Short: "Documents, their links and full-text search"}
	cmd.AddCommand(docAddCmd(opts), docLinkCmd(opts), docListCmd(opts),
		docSearchCmd(opts), docIndexCmd(opts), docReindexCmd(opts))
	return cmd
}

func docAddCmd(opts *GlobalOptions) *cobra.Command {
	var in ops.AddDocumentInput
	var bodyFile, sourceFile string
	var renditions, attachments []string

	cmd := &cobra.Command{
		Use:   "add <title>",
		Short: "Capture a file into the library and record a document for it",
		Long: "Capture a file into the library and record a document for it.\n\n" +
			"--file names a real file anywhere on disk; it is copied into the library\n" +
			"through the recoverable commit (stage, hash, immutable manifest, atomic\n" +
			"rename, seal - technical design §15), so the bytes end up under\n" +
			"library/YYYY/MM/DD because this command put them there, not because they\n" +
			"were pre-placed. --rendition and --attachment (repeatable) capture extra\n" +
			"files alongside it in the same commit, for cases like an .html original\n" +
			"with a .pdf rendition.\n\n" +
			"--path instead registers a file that is already inside the library at a\n" +
			"known relative path; it does not copy anything and cannot be combined\n" +
			"with --file/--rendition/--attachment.",
		Annotations: map[string]string{"write": "true"},
		Args:        cobra.MaximumNArgs(1),
	}
	cmd.Flags().StringVar(&in.Kind, "kind", "", "dossier|meeting_note|contract_doc|proposal|content_draft|release_note|decision|report|other")
	cmd.Flags().StringVar(&in.OccurredAt, "occurred", "", "when the thing it records happened, YYYY-MM-DD")
	cmd.Flags().StringVar(&in.CapturedAt, "captured", "", "when it entered the library, YYYY-MM-DD")
	cmd.Flags().StringVar(&in.ReviewAt, "review", "", "when to look at it again, YYYY-MM-DD")
	cmd.Flags().StringVar(&in.SupersedesID, "supersedes", "", "the document version this replaces")
	cmd.Flags().StringVar(&in.ChangeNote, "change-note", "", "what changed in this version")
	cmd.Flags().StringVar(&in.Source, "source", "", "where it came from")
	cmd.Flags().StringVar(&in.AuthorName, "author", "", "who wrote it")
	cmd.Flags().StringVar(&in.CanonicalURL, "url", "", "canonical URL, for external material")
	cmd.Flags().StringVar(&in.UserNote, "note", "", "why this was kept")
	cmd.Flags().StringVar(&sourceFile, "file", "", "path to a real file to capture into the library as the original")
	cmd.Flags().StringArrayVar(&renditions, "rendition", nil,
		"path to an additional rendition of the same document (e.g. a PDF export of an HTML original); repeatable")
	cmd.Flags().StringArrayVar(&attachments, "attachment", nil,
		"path to a supporting file to capture alongside the document; repeatable")
	cmd.Flags().StringVar(&in.RelPath, "path", "", "legacy: rel_path of a file already inside the library")
	cmd.Flags().StringVar(&in.Mime, "mime", "", "MIME type of the original (legacy --path only)")
	cmd.Flags().StringVar(&in.SHA256, "sha256", "", "sha256 of the original bytes (legacy --path only)")
	cmd.Flags().StringVar(&in.FileRole, "file-role", "", "original|rendition|attachment (legacy --path only)")
	cmd.Flags().StringVar(&bodyFile, "body-file", "",
		"read text from this file and index it, so the document is findable by content")

	cmd.RunE = runArgs(opts, func(rt *Runtime, args []string) int {
		const command = "doc.add"
		if len(args) == 1 {
			in.Title = args[0]
		}
		if rt.Opts.InputFile != "" {
			if err := rt.ReadInput(&in); err != nil {
				return rt.EmitError(command, err)
			}
		}
		// Read the body before writing anything: if the file is unreadable the
		// user should learn that now, not after a document row already exists.
		var body []byte
		if bodyFile != "" {
			b, err := os.ReadFile(bodyFile)
			if err != nil {
				return rt.EmitError(command, err)
			}
			body = b
		}
		store, err := rt.OpsStore(false)
		if err != nil {
			return rt.EmitError(command, err)
		}
		ctx, cancel := rt.Context()
		defer cancel()

		wc := rt.WriteContext()
		var result *ops.Result
		if sourceFile != "" || len(renditions) > 0 || len(attachments) > 0 {
			var atts []ops.AddDocumentAttachment
			for _, p := range renditions {
				atts = append(atts, ops.AddDocumentAttachment{SourcePath: p, Role: "rendition"})
			}
			for _, p := range attachments {
				atts = append(atts, ops.AddDocumentAttachment{SourcePath: p, Role: "attachment"})
			}
			result, err = store.CaptureDocument(ctx, wc, rt.Layout, ops.CaptureDocumentInput{
				AddDocumentInput: in,
				SourceFile:       sourceFile,
				Attachments:      atts,
			})
		} else {
			result, err = store.AddDocument(ctx, wc, in)
		}
		if err != nil {
			return rt.EmitError(command, err)
		}
		doc, _ := result.Data.(*ops.Document)
		if body != nil && doc != nil {
			// Indexing is a second write on purpose. It is derived data and can
			// be rebuilt from the file, so it must not be able to fail the
			// document creation it follows. It gets its own request id, derived
			// from the same base, rather than reusing wc.RequestID verbatim: two
			// writes sharing one id would make the second collide with the
			// first's idempotency record whenever the caller supplies an
			// explicit --request-id (the auto-generated case never collides,
			// since every WriteContext() call mints a fresh one).
			indexWC := wc
			indexWC.RequestID = wc.RequestID + ":index"
			if _, err := store.IndexDocument(ctx, indexWC, ops.IndexDocumentInput{
				DocumentID: doc.ID, Body: string(body),
			}); err != nil {
				return rt.EmitError(command, err)
			}
		}
		return rt.Emit(command, result, func(w io.Writer, data any) error {
			if d, ok := data.(*ops.Document); ok {
				indexed := ""
				if body != nil {
					indexed = fmt.Sprintf("  (indexed %d bytes)", len(body))
				}
				fmt.Fprintf(w, "added %s  %s%s\n", d.ID, d.Title, indexed)
			}
			return nil
		})
	})
	return cmd
}

func docLinkCmd(opts *GlobalOptions) *cobra.Command {
	var in ops.LinkDocumentInput
	cmd := &cobra.Command{
		Use:         "link <document-id>",
		Short:       "Attach a document to a business object",
		Annotations: map[string]string{"write": "true"},
		Args:        cobra.MaximumNArgs(1),
	}
	cmd.Flags().StringVar(&in.EntityType, "entity-type", "", ops.EntityTypeList())
	cmd.Flags().StringVar(&in.EntityID, "entity", "", "id of the object to attach it to")
	cmd.Flags().StringVar(&in.LinkType, "as", "", "dossier|minutes|evidence|attachment|deliverable")

	cmd.RunE = runArgs(opts, func(rt *Runtime, args []string) int {
		const command = "doc.link"
		if len(args) == 1 {
			in.DocID = args[0]
		}
		if rt.Opts.InputFile != "" {
			if err := rt.ReadInput(&in); err != nil {
				return rt.EmitError(command, err)
			}
		}
		store, err := rt.OpsStore(false)
		if err != nil {
			return rt.EmitError(command, err)
		}
		ctx, cancel := rt.Context()
		defer cancel()
		result, err := store.LinkDocument(ctx, rt.WriteContext(), in)
		if err != nil {
			return rt.EmitError(command, err)
		}
		return rt.Emit(command, result, func(w io.Writer, data any) error {
			fmt.Fprintf(w, "linked %s to %s %s\n", in.DocID, in.EntityType, in.EntityID)
			return nil
		})
	})
	return cmd
}

func docListCmd(opts *GlobalOptions) *cobra.Command {
	var f ops.DocumentFilter
	cmd := &cobra.Command{Use: "list", Short: "List documents"}
	cmd.Flags().StringVar(&f.Kind, "kind", "", "filter by kind")
	cmd.Flags().StringVar(&f.Search, "search", "", "match against the title")
	cmd.Flags().IntVar(&f.Limit, "limit", 0, "maximum rows")

	cmd.RunE = run(opts, func(rt *Runtime) int {
		const command = "doc.list"
		store, err := rt.OpsStore(true)
		if err != nil {
			return rt.EmitError(command, err)
		}
		ctx, cancel := rt.Context()
		defer cancel()
		docs, err := store.ListDocuments(ctx, f)
		if err != nil {
			return rt.EmitError(command, err)
		}
		return rt.EmitData(command, docs, func(w io.Writer, data any) error {
			list, _ := data.([]*ops.Document)
			if len(list) == 0 {
				fmt.Fprintln(w, "(none)")
				return nil
			}
			for _, d := range list {
				fmt.Fprintf(w, "%-24s  %-14s  %s\n", d.ID, d.Kind, d.Title)
			}
			return nil
		})
	})
	return cmd
}

func docSearchCmd(opts *GlobalOptions) *cobra.Command {
	var limit int
	cmd := &cobra.Command{
		Use:   "search <query>",
		Short: "Find documents by content",
		Long: "Find documents by content.\n\n" +
			"Queries of three characters or more use the FTS5 trigram index. Shorter " +
			"queries cannot form a trigram, so they fall back to a slower scan - which " +
			"is how a two-character name like 杨总 is found at all. The result reports " +
			"which path answered.\n\n" +
			"Every version of a document matches, not just the newest. Superseded hits " +
			"are marked and name the version to read instead; they are not hidden, " +
			"because a replaced conclusion is still evidence of what was decided when.",
		Args: cobra.ExactArgs(1),
	}
	cmd.Flags().IntVar(&limit, "limit", 0, "maximum hits")

	cmd.RunE = runArgs(opts, func(rt *Runtime, args []string) int {
		const command = "doc.search"
		store, err := rt.OpsStore(true)
		if err != nil {
			return rt.EmitError(command, err)
		}
		ctx, cancel := rt.Context()
		defer cancel()
		res, err := store.SearchDocuments(ctx, args[0], limit)
		if err != nil {
			return rt.EmitError(command, err)
		}
		return rt.EmitData(command, res, func(w io.Writer, data any) error {
			r, ok := data.(*ops.DocumentSearchResult)
			if !ok {
				return nil
			}
			if len(r.Hits) == 0 {
				fmt.Fprintf(w, "no documents match %q (%s)\n", r.Query, r.Mode)
				return nil
			}
			fmt.Fprintf(w, "%d hit(s) for %q via %s", len(r.Hits), r.Query, r.Mode)
			if r.SupersededHits > 0 {
				fmt.Fprintf(w, "; %d superseded", r.SupersededHits)
			}
			fmt.Fprintln(w)
			for _, h := range r.Hits {
				fmt.Fprintf(w, "  %-24s  %-14s  %s\n", h.DocID, h.Kind, h.Title)
				// The two ways a hit can be out of date, spelled out rather
				// than left to a flag the eye skips: replaced by a newer
				// version, or past the date it asked to be re-examined.
				if !h.IsCurrent {
					fmt.Fprintf(w, "      superseded -> read %s instead\n", h.SupersededBy)
				}
				if h.ReviewDue {
					fmt.Fprintf(w, "      due for review since %s\n", h.ReviewAt)
				}
				if h.Snippet != "" {
					fmt.Fprintf(w, "      %s\n", h.Snippet)
				}
			}
			return nil
		})
	})
	return cmd
}

func docIndexCmd(opts *GlobalOptions) *cobra.Command {
	var bodyFile string
	cmd := &cobra.Command{
		Use:         "index <document-id>",
		Short:       "Index a document's text so it can be found by content",
		Annotations: map[string]string{"write": "true"},
		Args:        cobra.ExactArgs(1),
	}
	cmd.Flags().StringVar(&bodyFile, "body-file", "", "file to read the text from (required)")

	cmd.RunE = runArgs(opts, func(rt *Runtime, args []string) int {
		const command = "doc.index"
		if bodyFile == "" {
			return rt.EmitError(command, errBodyFileRequired)
		}
		body, err := os.ReadFile(bodyFile)
		if err != nil {
			return rt.EmitError(command, err)
		}
		store, err := rt.OpsStore(false)
		if err != nil {
			return rt.EmitError(command, err)
		}
		ctx, cancel := rt.Context()
		defer cancel()
		result, err := store.IndexDocument(ctx, rt.WriteContext(), ops.IndexDocumentInput{
			DocumentID: args[0], Body: string(body),
		})
		if err != nil {
			return rt.EmitError(command, err)
		}
		return rt.Emit(command, result, func(w io.Writer, data any) error {
			fmt.Fprintf(w, "indexed %s  (%d bytes)\n", args[0], len(body))
			return nil
		})
	})
	return cmd
}

func docReindexCmd(opts *GlobalOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "reindex",
		Short: "List documents whose indexed text is missing or out of date",
		Long: "List documents whose search index needs work, and why.\n\n" +
			"  not_indexed         an original file exists, its text was never supplied.\n" +
			"                      Findable by title and metadata, not by content.\n" +
			"  content_changed     the original file changed after indexing, so search\n" +
			"                      is serving text that is no longer the document's.\n" +
			"  provenance_unknown  indexed text with no recorded hash behind it. It may\n" +
			"                      well be current; nothing recorded can establish that.\n\n" +
			"Re-indexing needs the file's text, so this reports what to feed back " +
			"through `doc index` rather than reading the library itself.",
	}
	cmd.RunE = run(opts, func(rt *Runtime) int {
		const command = "doc.reindex"
		store, err := rt.OpsStore(true)
		if err != nil {
			return rt.EmitError(command, err)
		}
		ctx, cancel := rt.Context()
		defer cancel()
		queue, err := store.DocumentsNeedingIndex(ctx)
		if err != nil {
			return rt.EmitError(command, err)
		}
		return rt.EmitData(command, queue, func(w io.Writer, data any) error {
			list, _ := data.([]ops.IndexQueueEntry)
			if len(list) == 0 {
				fmt.Fprintln(w, "every document body is indexed and current")
				return nil
			}
			fmt.Fprintf(w, "%d document(s) need indexing (%s):\n", len(list), indexQueueSummary(list))
			for _, e := range list {
				fmt.Fprintf(w, "  %-20s  %-24s  %-40s  %s\n", e.Reason, e.DocID, e.Title, e.RelPath)
			}
			return nil
		})
	})
	return cmd
}

// indexQueueSummary counts the queue by reason, in a fixed order so repeated
// runs read the same way. Unknown reasons are not swallowed: a value the CLI
// does not recognise still gets counted under its own name.
func indexQueueSummary(queue []ops.IndexQueueEntry) string {
	counts := map[string]int{}
	var order []string
	for _, r := range []string{ops.IndexReasonNotIndexed, ops.IndexReasonContentChanged,
		ops.IndexReasonProvenanceUnknown} {
		counts[r] = 0
		order = append(order, r)
	}
	for _, e := range queue {
		if _, known := counts[e.Reason]; !known {
			order = append(order, e.Reason)
		}
		counts[e.Reason]++
	}
	var parts []string
	for _, r := range order {
		if counts[r] > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", counts[r], r))
		}
	}
	return strings.Join(parts, ", ")
}
