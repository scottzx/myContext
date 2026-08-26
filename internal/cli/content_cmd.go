package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/scottzx/mycontext/internal/ops"
)

// ---------------------------------------------------------------------------
// mycontext channel create|list|update
// ---------------------------------------------------------------------------

func newChannelCmd(opts *GlobalOptions) *cobra.Command {
	cmd := &cobra.Command{Use: "channel", Short: "Our own publishing accounts (小红书|公众号|抖音|B站|X|线下)"}
	cmd.AddCommand(channelCreateCmd(opts), channelListCmd(opts), channelUpdateCmd(opts))
	return cmd
}

func channelLine(c *ops.Channel) string {
	return fmt.Sprintf("%s  %s  [%s %s]  handle=%s  v%d", c.ID, c.Name, c.Platform, c.Status, Deref(c.Handle), c.Version)
}

func channelCreateCmd(opts *GlobalOptions) *cobra.Command {
	var in ops.CreateChannelInput
	cmd := &cobra.Command{
		Use:         "create <name>",
		Short:       "Register one of our own publishing accounts",
		Annotations: map[string]string{"write": "true"},
		Args:        cobra.MaximumNArgs(1),
	}
	cmd.Flags().StringVar(&in.Platform, "platform", "", "xiaohongshu|wechat|douyin|bilibili|x|offline (required)")
	cmd.Flags().StringVar(&in.Handle, "handle", "", "the account id on that platform")
	cmd.Flags().StringVar(&in.Status, "status", "", "active|paused|archived (default active)")
	cmd.Flags().StringVar(&in.Owner, "owner", "", "who runs this account")
	cmd.Flags().StringVar(&in.Note, "note", "", "note")
	cmd.Flags().StringVar(&in.LegacyRef, "legacy-ref", "", "reference into the legacy data source")
	cmd.MarkFlagRequired("platform")

	cmd.RunE = runArgs(opts, func(rt *Runtime, args []string) int {
		const command = "channel.create"
		if len(args) == 1 {
			in.Name = args[0]
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
		result, err := store.CreateChannel(ctx, rt.WriteContext(), in)
		if err != nil {
			return rt.EmitError(command, err)
		}
		return rt.Emit(command, result, func(w io.Writer, data any) error {
			if c, ok := data.(*ops.Channel); ok {
				fmt.Fprintf(w, "created %s  %s\n", c.ID, c.Name)
			}
			return nil
		})
	})
	return cmd
}

func channelUpdateCmd(opts *GlobalOptions) *cobra.Command {
	var in ops.UpdateChannelInput
	var name, platform, handle, status, owner, note string

	cmd := &cobra.Command{
		Use:         "update <id>",
		Short:       "Patch a channel (requires --expected-version)",
		Annotations: map[string]string{"write": "true"},
		Args:        cobra.ExactArgs(1),
	}
	cmd.Flags().Int64Var(&in.ExpectedVersion, "expected-version", 0, "version read before editing")
	cmd.Flags().StringVar(&name, "name", "", "new name")
	cmd.Flags().StringVar(&platform, "platform", "", "xiaohongshu|wechat|douyin|bilibili|x|offline")
	cmd.Flags().StringVar(&handle, "handle", "", "the account id on that platform")
	cmd.Flags().StringVar(&status, "status", "", "active|paused|archived")
	cmd.Flags().StringVar(&owner, "owner", "", "who runs this account")
	cmd.Flags().StringVar(&note, "note", "", "note")

	cmd.RunE = runArgs(opts, func(rt *Runtime, args []string) int {
		const command = "channel.update"
		in.ChannelID = args[0]
		setIfChanged(cmd, "name", name, &in.Name)
		setIfChanged(cmd, "platform", platform, &in.Platform)
		setIfChanged(cmd, "handle", handle, &in.Handle)
		setIfChanged(cmd, "status", status, &in.Status)
		setIfChanged(cmd, "owner", owner, &in.Owner)
		setIfChanged(cmd, "note", note, &in.Note)

		store, err := rt.OpsStore(false)
		if err != nil {
			return rt.EmitError(command, err)
		}
		ctx, cancel := rt.Context()
		defer cancel()
		if in.ExpectedVersion == 0 && rt.Opts.Actor != "agent" {
			channel, err := store.GetChannel(ctx, in.ChannelID)
			if err != nil {
				return rt.EmitError(command, err)
			}
			in.ExpectedVersion = channel.Version
		}
		result, err := store.UpdateChannel(ctx, rt.WriteContext(), in)
		if err != nil {
			return rt.EmitError(command, err)
		}
		return rt.Emit(command, result, func(w io.Writer, data any) error {
			if c, ok := data.(*ops.Channel); ok {
				fmt.Fprintln(w, channelLine(c))
			}
			return nil
		})
	})
	return cmd
}

func channelListCmd(opts *GlobalOptions) *cobra.Command {
	var f ops.ChannelFilter
	cmd := &cobra.Command{Use: "list", Short: "List channels, most recently updated first"}
	cmd.Flags().StringVar(&f.Platform, "platform", "", "filter by platform")
	cmd.Flags().StringVar(&f.Status, "status", "", "filter by status")
	cmd.Flags().StringVar(&f.Search, "search", "", "match name")
	cmd.Flags().IntVar(&f.Limit, "limit", 200, "maximum rows")

	cmd.RunE = run(opts, func(rt *Runtime) int {
		const command = "channel.list"
		store, err := rt.OpsStore(true)
		if err != nil {
			return rt.EmitError(command, err)
		}
		ctx, cancel := rt.Context()
		defer cancel()
		items, err := store.ListChannels(ctx, f)
		if err != nil {
			return rt.EmitError(command, err)
		}
		return rt.EmitData(command, items, func(w io.Writer, _ any) error {
			rows := make([][]string, 0, len(items))
			for _, c := range items {
				rows = append(rows, []string{c.ID, c.Platform, c.Status, Deref(c.Handle), c.Name})
			}
			return Table(w, []string{"ID", "PLATFORM", "STATUS", "HANDLE", "NAME"}, rows)
		})
	})
	return cmd
}

// ---------------------------------------------------------------------------
// mycontext content create|update|publish|list
// ---------------------------------------------------------------------------

func newContentCmd(opts *GlobalOptions) *cobra.Command {
	cmd := &cobra.Command{Use: "content", Short: "One post, from 选题 (status=idea) to published"}
	cmd.AddCommand(contentCreateCmd(opts), contentUpdateCmd(opts), contentPublishCmd(opts), contentListCmd(opts))
	return cmd
}

func contentPieceLine(c *ops.ContentPiece) string {
	return fmt.Sprintf("%s  %s  [%s]  channel=%s  v%d", c.ID, c.Title, c.Status, Deref(c.ChannelID), c.Version)
}

// fillContentPieceVersion is fillOpportunityVersion's twin for content
// pieces: an interactive caller gets the current version read for them, an
// agent still must pass --expected-version itself.
func fillContentPieceVersion(ctx context.Context, store *ops.Store, actor, id string, version *int64) error {
	if *version != 0 || actor == "agent" {
		return nil
	}
	cp, err := store.GetContentPiece(ctx, id)
	if err != nil {
		return err
	}
	*version = cp.Version
	return nil
}

func contentCreateCmd(opts *GlobalOptions) *cobra.Command {
	var in ops.CreateContentPieceInput
	cmd := &cobra.Command{
		Use:         "create <title>",
		Short:       "Record a 选题: a new piece starts at status=idea",
		Annotations: map[string]string{"write": "true"},
		Args:        cobra.MaximumNArgs(1),
	}
	cmd.Flags().StringVar(&in.ChannelID, "channel", "", "channel id, if one is already decided")
	cmd.Flags().StringVar(&in.Topic, "topic", "", "the angle, in the writer's words")
	cmd.Flags().StringVar(&in.ProductID, "product", "", "product id this piece promotes")
	cmd.Flags().StringVar(&in.CampaignID, "campaign", "", "campaign id this piece belongs to")
	cmd.Flags().StringVar(&in.Owner, "owner", "", "who is writing this")
	cmd.Flags().StringVar(&in.Note, "note", "", "note")
	cmd.Flags().StringVar(&in.LegacyRef, "legacy-ref", "", "reference into the legacy data source")

	cmd.RunE = runArgs(opts, func(rt *Runtime, args []string) int {
		const command = "content.create"
		if len(args) == 1 {
			in.Title = args[0]
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
		result, err := store.CreateContentPiece(ctx, rt.WriteContext(), in)
		if err != nil {
			return rt.EmitError(command, err)
		}
		return rt.Emit(command, result, func(w io.Writer, data any) error {
			if c, ok := data.(*ops.ContentPiece); ok {
				fmt.Fprintf(w, "created %s  %s  [idea]\n", c.ID, c.Title)
			}
			return nil
		})
	})
	return cmd
}

func contentUpdateCmd(opts *GlobalOptions) *cobra.Command {
	var in ops.UpdateContentPieceInput
	var title, topic, channel, product, campaign, status, scheduledFor, owner, note string

	cmd := &cobra.Command{
		Use:         "update <id>",
		Short:       "Patch a content piece (requires --expected-version; use `content publish` to publish)",
		Annotations: map[string]string{"write": "true"},
		Args:        cobra.ExactArgs(1),
	}
	cmd.Flags().Int64Var(&in.ExpectedVersion, "expected-version", 0, "version read before editing")
	cmd.Flags().StringVar(&title, "title", "", "new title")
	cmd.Flags().StringVar(&topic, "topic", "", "the angle, in the writer's words")
	cmd.Flags().StringVar(&channel, "channel", "", "channel id")
	cmd.Flags().StringVar(&product, "product", "", "product id this piece promotes")
	cmd.Flags().StringVar(&campaign, "campaign", "", "campaign id this piece belongs to")
	cmd.Flags().StringVar(&status, "status", "", "idea|drafting|review|scheduled|archived (published goes through `content publish`)")
	cmd.Flags().StringVar(&scheduledFor, "scheduled-for", "", "when it is scheduled to publish, RFC 3339")
	cmd.Flags().StringVar(&owner, "owner", "", "who is writing this")
	cmd.Flags().StringVar(&note, "note", "", "note")

	cmd.RunE = runArgs(opts, func(rt *Runtime, args []string) int {
		const command = "content.update"
		in.ContentPieceID = args[0]
		setIfChanged(cmd, "title", title, &in.Title)
		setIfChanged(cmd, "topic", topic, &in.Topic)
		setIfChanged(cmd, "channel", channel, &in.ChannelID)
		setIfChanged(cmd, "product", product, &in.ProductID)
		setIfChanged(cmd, "campaign", campaign, &in.CampaignID)
		setIfChanged(cmd, "status", status, &in.Status)
		setIfChanged(cmd, "scheduled-for", scheduledFor, &in.ScheduledFor)
		setIfChanged(cmd, "owner", owner, &in.Owner)
		setIfChanged(cmd, "note", note, &in.Note)

		store, err := rt.OpsStore(false)
		if err != nil {
			return rt.EmitError(command, err)
		}
		ctx, cancel := rt.Context()
		defer cancel()
		if err := fillContentPieceVersion(ctx, store, rt.Opts.Actor, in.ContentPieceID, &in.ExpectedVersion); err != nil {
			return rt.EmitError(command, err)
		}
		result, err := store.UpdateContentPiece(ctx, rt.WriteContext(), in)
		if err != nil {
			return rt.EmitError(command, err)
		}
		return rt.Emit(command, result, func(w io.Writer, data any) error {
			if c, ok := data.(*ops.ContentPiece); ok {
				fmt.Fprintln(w, contentPieceLine(c))
			}
			return nil
		})
	})
	return cmd
}

func contentPublishCmd(opts *GlobalOptions) *cobra.Command {
	var in ops.PublishContentPieceInput
	cmd := &cobra.Command{
		Use:         "publish <id>",
		Short:       "Publish a content piece: sets published_at and the url",
		Annotations: map[string]string{"write": "true"},
		Args:        cobra.ExactArgs(1),
	}
	cmd.Flags().Int64Var(&in.ExpectedVersion, "expected-version", 0, "version read before editing")
	cmd.Flags().StringVar(&in.URL, "url", "", "the public URL of the published piece")
	cmd.Flags().StringVar(&in.PublishedAt, "at", "", "when it was published, RFC 3339 (default now)")

	cmd.RunE = runArgs(opts, func(rt *Runtime, args []string) int {
		const command = "content.publish"
		in.ContentPieceID = args[0]
		store, err := rt.OpsStore(false)
		if err != nil {
			return rt.EmitError(command, err)
		}
		ctx, cancel := rt.Context()
		defer cancel()
		if err := fillContentPieceVersion(ctx, store, rt.Opts.Actor, in.ContentPieceID, &in.ExpectedVersion); err != nil {
			return rt.EmitError(command, err)
		}
		result, err := store.PublishContentPiece(ctx, rt.WriteContext(), in)
		if err != nil {
			return rt.EmitError(command, err)
		}
		return rt.Emit(command, result, func(w io.Writer, data any) error {
			if c, ok := data.(*ops.ContentPiece); ok {
				fmt.Fprintf(w, "published %s  %s  %s\n", c.ID, c.Title, Deref(c.URL))
			}
			return nil
		})
	})
	return cmd
}

func contentListCmd(opts *GlobalOptions) *cobra.Command {
	var f ops.ContentPieceFilter
	cmd := &cobra.Command{Use: "list", Short: "List content pieces, most recently updated first"}
	cmd.Flags().StringVar(&f.ChannelID, "channel", "", "filter by channel id")
	cmd.Flags().StringVar(&f.Status, "status", "", "filter by status")
	cmd.Flags().StringVar(&f.ProductID, "product", "", "filter by product id")
	cmd.Flags().StringVar(&f.CampaignID, "campaign", "", "filter by campaign id")
	cmd.Flags().StringVar(&f.Search, "search", "", "match title")
	cmd.Flags().IntVar(&f.Limit, "limit", 200, "maximum rows")

	cmd.RunE = run(opts, func(rt *Runtime) int {
		const command = "content.list"
		store, err := rt.OpsStore(true)
		if err != nil {
			return rt.EmitError(command, err)
		}
		ctx, cancel := rt.Context()
		defer cancel()
		items, err := store.ListContentPieces(ctx, f)
		if err != nil {
			return rt.EmitError(command, err)
		}
		return rt.EmitData(command, items, func(w io.Writer, _ any) error {
			rows := make([][]string, 0, len(items))
			for _, c := range items {
				rows = append(rows, []string{c.ID, c.Status, Deref(c.ChannelID), Deref(c.ScheduledFor), c.Title})
			}
			return Table(w, []string{"ID", "STATUS", "CHANNEL", "SCHEDULED", "TITLE"}, rows)
		})
	})
	return cmd
}

// ---------------------------------------------------------------------------
// mycontext release create|ship|list
// ---------------------------------------------------------------------------

func newReleaseCmd(opts *GlobalOptions) *cobra.Command {
	cmd := &cobra.Command{Use: "release", Short: "One shipped version of a product"}
	cmd.AddCommand(releaseCreateCmd(opts), releaseShipCmd(opts), releaseListCmd(opts))
	return cmd
}

func releaseLine(r *ops.Release) string {
	return fmt.Sprintf("%s  %s  [%s]  released=%s  v%d", r.ID, r.VersionLabel, r.Status, Deref(r.ReleasedAt), r.Version)
}

func releaseCreateCmd(opts *GlobalOptions) *cobra.Command {
	var in ops.CreateReleaseInput
	cmd := &cobra.Command{
		Use:         "create <version-label>",
		Short:       "Record a planned or in-progress version (use `release ship` to mark it released)",
		Annotations: map[string]string{"write": "true"},
		Args:        cobra.MaximumNArgs(1),
	}
	cmd.Flags().StringVar(&in.ProductID, "product", "", "product id (required)")
	cmd.Flags().StringVar(&in.Status, "status", "", "planned|developing (default planned)")
	cmd.Flags().StringVar(&in.Note, "note", "", "note")
	cmd.Flags().StringVar(&in.LegacyRef, "legacy-ref", "", "reference into the legacy data source")
	cmd.MarkFlagRequired("product")

	cmd.RunE = runArgs(opts, func(rt *Runtime, args []string) int {
		const command = "release.create"
		if len(args) == 1 {
			in.VersionLabel = args[0]
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
		result, err := store.CreateRelease(ctx, rt.WriteContext(), in)
		if err != nil {
			return rt.EmitError(command, err)
		}
		return rt.Emit(command, result, func(w io.Writer, data any) error {
			if r, ok := data.(*ops.Release); ok {
				fmt.Fprintf(w, "created %s  %s  [%s]\n", r.ID, r.VersionLabel, r.Status)
			}
			return nil
		})
	})
	return cmd
}

func releaseShipCmd(opts *GlobalOptions) *cobra.Command {
	var in ops.ShipReleaseInput
	cmd := &cobra.Command{
		Use:         "ship <id>",
		Short:       "Mark a release shipped (requires --expected-version)",
		Annotations: map[string]string{"write": "true"},
		Args:        cobra.ExactArgs(1),
	}
	cmd.Flags().Int64Var(&in.ExpectedVersion, "expected-version", 0, "version read before editing")
	cmd.Flags().StringVar(&in.ReleasedAt, "at", "", "when it was released, RFC 3339 (default now)")

	cmd.RunE = runArgs(opts, func(rt *Runtime, args []string) int {
		const command = "release.ship"
		in.ReleaseID = args[0]
		store, err := rt.OpsStore(false)
		if err != nil {
			return rt.EmitError(command, err)
		}
		ctx, cancel := rt.Context()
		defer cancel()
		if in.ExpectedVersion == 0 && rt.Opts.Actor != "agent" {
			r, err := store.GetRelease(ctx, in.ReleaseID)
			if err != nil {
				return rt.EmitError(command, err)
			}
			in.ExpectedVersion = r.Version
		}
		result, err := store.ShipRelease(ctx, rt.WriteContext(), in)
		if err != nil {
			return rt.EmitError(command, err)
		}
		return rt.Emit(command, result, func(w io.Writer, data any) error {
			if r, ok := data.(*ops.Release); ok {
				fmt.Fprintln(w, releaseLine(r))
			}
			return nil
		})
	})
	return cmd
}

func releaseListCmd(opts *GlobalOptions) *cobra.Command {
	var f ops.ReleaseFilter
	cmd := &cobra.Command{Use: "list", Short: "List releases, newest first"}
	cmd.Flags().StringVar(&f.ProductID, "product", "", "filter by product id")
	cmd.Flags().StringVar(&f.Status, "status", "", "filter by status")
	cmd.Flags().IntVar(&f.Limit, "limit", 200, "maximum rows")

	cmd.RunE = run(opts, func(rt *Runtime) int {
		const command = "release.list"
		store, err := rt.OpsStore(true)
		if err != nil {
			return rt.EmitError(command, err)
		}
		ctx, cancel := rt.Context()
		defer cancel()
		items, err := store.ListReleases(ctx, f)
		if err != nil {
			return rt.EmitError(command, err)
		}
		return rt.EmitData(command, items, func(w io.Writer, _ any) error {
			rows := make([][]string, 0, len(items))
			for _, r := range items {
				rows = append(rows, []string{r.ID, r.ProductID, r.VersionLabel, r.Status, Deref(r.ReleasedAt)})
			}
			return Table(w, []string{"ID", "PRODUCT", "VERSION", "STATUS", "RELEASED"}, rows)
		})
	})
	return cmd
}

// ---------------------------------------------------------------------------
// mycontext campaign create|close|list
// ---------------------------------------------------------------------------

func newCampaignCmd(opts *GlobalOptions) *cobra.Command {
	cmd := &cobra.Command{Use: "campaign", Short: "One promotion push, online or offline"}
	cmd.AddCommand(campaignCreateCmd(opts), campaignCloseCmd(opts), campaignListCmd(opts))
	return cmd
}

func campaignLine(c *ops.Campaign) string {
	return fmt.Sprintf("%s  %s  [%s %s]  budget=%s  v%d", c.ID, c.Name, c.ChannelType, c.Status, floatOrDash(c.Budget), c.Version)
}

func campaignCreateCmd(opts *GlobalOptions) *cobra.Command {
	var in ops.CreateCampaignInput
	var budget float64
	cmd := &cobra.Command{
		Use:         "create <name>",
		Short:       "Record a promotion push",
		Annotations: map[string]string{"write": "true"},
		Args:        cobra.MaximumNArgs(1),
	}
	cmd.Flags().StringVar(&in.ProductID, "product", "", "product id this push promotes")
	cmd.Flags().StringVar(&in.ChannelType, "channel-type", "", "online|offline (default online)")
	cmd.Flags().StringVar(&in.StartDate, "start", "", "start date, YYYY-MM-DD")
	cmd.Flags().StringVar(&in.EndDate, "end", "", "end date, YYYY-MM-DD")
	cmd.Flags().Float64Var(&budget, "budget", 0, "planned spend")
	cmd.Flags().StringVar(&in.Owner, "owner", "", "who owns this push")
	cmd.Flags().StringVar(&in.Note, "note", "", "note")
	cmd.Flags().StringVar(&in.LegacyRef, "legacy-ref", "", "reference into the legacy data source")

	cmd.RunE = runArgs(opts, func(rt *Runtime, args []string) int {
		const command = "campaign.create"
		if len(args) == 1 {
			in.Name = args[0]
		}
		bindFloat(cmd, "budget", &budget, &in.Budget)
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
		result, err := store.CreateCampaign(ctx, rt.WriteContext(), in)
		if err != nil {
			return rt.EmitError(command, err)
		}
		return rt.Emit(command, result, func(w io.Writer, data any) error {
			if c, ok := data.(*ops.Campaign); ok {
				fmt.Fprintf(w, "created %s  %s  [%s]\n", c.ID, c.Name, c.Status)
			}
			return nil
		})
	})
	return cmd
}

func campaignCloseCmd(opts *GlobalOptions) *cobra.Command {
	var in ops.CloseCampaignInput
	cmd := &cobra.Command{
		Use:         "close <id>",
		Short:       "Mark a campaign ended (requires --expected-version and a start_date already set)",
		Annotations: map[string]string{"write": "true"},
		Args:        cobra.ExactArgs(1),
	}
	cmd.Flags().Int64Var(&in.ExpectedVersion, "expected-version", 0, "version read before editing")
	cmd.Flags().StringVar(&in.EndDate, "end", "", "end date, YYYY-MM-DD (default today)")

	cmd.RunE = runArgs(opts, func(rt *Runtime, args []string) int {
		const command = "campaign.close"
		in.CampaignID = args[0]
		store, err := rt.OpsStore(false)
		if err != nil {
			return rt.EmitError(command, err)
		}
		ctx, cancel := rt.Context()
		defer cancel()
		if in.ExpectedVersion == 0 && rt.Opts.Actor != "agent" {
			c, err := store.GetCampaign(ctx, in.CampaignID)
			if err != nil {
				return rt.EmitError(command, err)
			}
			in.ExpectedVersion = c.Version
		}
		result, err := store.CloseCampaign(ctx, rt.WriteContext(), in)
		if err != nil {
			return rt.EmitError(command, err)
		}
		return rt.Emit(command, result, func(w io.Writer, data any) error {
			if c, ok := data.(*ops.Campaign); ok {
				fmt.Fprintln(w, campaignLine(c))
			}
			return nil
		})
	})
	return cmd
}

func campaignListCmd(opts *GlobalOptions) *cobra.Command {
	var f ops.CampaignFilter
	cmd := &cobra.Command{Use: "list", Short: "List campaigns, most recently updated first"}
	cmd.Flags().StringVar(&f.ProductID, "product", "", "filter by product id")
	cmd.Flags().StringVar(&f.Status, "status", "", "filter by status")
	cmd.Flags().StringVar(&f.ChannelType, "channel-type", "", "filter by channel_type")
	cmd.Flags().StringVar(&f.Search, "search", "", "match name")
	cmd.Flags().IntVar(&f.Limit, "limit", 200, "maximum rows")

	cmd.RunE = run(opts, func(rt *Runtime) int {
		const command = "campaign.list"
		store, err := rt.OpsStore(true)
		if err != nil {
			return rt.EmitError(command, err)
		}
		ctx, cancel := rt.Context()
		defer cancel()
		items, err := store.ListCampaigns(ctx, f)
		if err != nil {
			return rt.EmitError(command, err)
		}
		return rt.EmitData(command, items, func(w io.Writer, _ any) error {
			rows := make([][]string, 0, len(items))
			for _, c := range items {
				rows = append(rows, []string{c.ID, c.ChannelType, c.Status, Deref(c.StartDate), Deref(c.EndDate), c.Name})
			}
			return Table(w, []string{"ID", "TYPE", "STATUS", "START", "END", "NAME"}, rows)
		})
	})
	return cmd
}
