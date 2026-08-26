package cli

import (
	"fmt"
	"io"
	"os"

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
	var bodyFile string

	cmd := &cobra.Command{
		Use:         "add <title>",
		Short:       "Record a document and optionally index its text",
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
	cmd.Flags().StringVar(&in.RelPath, "path", "", "path of the original file, relative to the library root")
	cmd.Flags().StringVar(&in.Mime, "mime", "", "MIME type of the original")
	cmd.Flags().StringVar(&in.SHA256, "sha256", "", "sha256 of the original bytes")
	cmd.Flags().StringVar(&in.FileRole, "file-role", "", "original|rendition|attachment")
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

		result, err := store.AddDocument(ctx, rt.WriteContext(), in)
		if err != nil {
			return rt.EmitError(command, err)
		}
		doc, _ := result.Data.(*ops.Document)
		if body != nil && doc != nil {
			// Indexing is a second write on purpose. It is derived data and can
			// be rebuilt from the file, so it must not be able to fail the
			// document creation it follows.
			if _, err := store.IndexDocument(ctx, rt.WriteContext(), ops.IndexDocumentInput{
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
			"which path answered.",
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
			fmt.Fprintf(w, "%d hit(s) for %q via %s\n", len(r.Hits), r.Query, r.Mode)
			for _, h := range r.Hits {
				fmt.Fprintf(w, "  %-24s  %-14s  %s\n", h.DocID, h.Kind, h.Title)
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
		Short: "List documents whose text is not in the search index",
		Long: "List documents that have an original file but no indexed text.\n\n" +
			"They are still findable by title and metadata, just not by content. " +
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
		missing, err := store.UnindexedDocuments(ctx)
		if err != nil {
			return rt.EmitError(command, err)
		}
		return rt.EmitData(command, missing, func(w io.Writer, data any) error {
			list, _ := data.([]ops.DocumentHit)
			if len(list) == 0 {
				fmt.Fprintln(w, "every document body is indexed")
				return nil
			}
			fmt.Fprintf(w, "%d document(s) need indexing:\n", len(list))
			for _, h := range list {
				fmt.Fprintf(w, "  %-24s  %-40s  %s\n", h.DocID, h.Title, h.RelPath)
			}
			return nil
		})
	})
	return cmd
}
