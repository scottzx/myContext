package cli

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/scottzx/mycontext/internal/ops"
	"github.com/scottzx/mycontext/internal/protocol"
)

// errBodyFileRequired is shared by the doc commands that need text to index.
var errBodyFileRequired = protocol.BadInput("--body-file is required")

// newMetricCmd records and reads the generic metric time series. Followers,
// impressions, DAU, monthly revenue and platform splits all land here rather
// than as columns, because they are observations over time and a single
// current_value column cannot answer "is it going up".
func newMetricCmd(opts *GlobalOptions) *cobra.Command {
	cmd := &cobra.Command{Use: "metric", Short: "Record and read metric observations over time"}
	cmd.AddCommand(metricRecordCmd(opts), metricTrendCmd(opts))
	return cmd
}

func metricRecordCmd(opts *GlobalOptions) *cobra.Command {
	var in ops.RecordMetricSampleInput
	var value float64

	cmd := &cobra.Command{
		Use:         "record <subject-id>",
		Short:       "Record one observation of a metric",
		Annotations: map[string]string{"write": "true"},
		Args:        cobra.MaximumNArgs(1),
	}
	cmd.Flags().StringVar(&in.SubjectType, "subject-type", "", ops.EntityTypeList())
	cmd.Flags().StringVar(&in.MetricName, "name", "", "what is being measured")
	cmd.Flags().Float64Var(&value, "value", 0, "the observed value")
	cmd.Flags().StringVar(&in.SampledAt, "at", "", "when it was observed, YYYY-MM-DD")
	cmd.Flags().StringVar(&in.Unit, "unit", "", "unit of the value")
	cmd.Flags().StringVar(&in.Source, "source", "", "where the number came from")

	cmd.RunE = runArgs(opts, func(rt *Runtime, args []string) int {
		const command = "metric.record"
		if len(args) == 1 {
			in.SubjectID = args[0]
		}
		if cmd.Flags().Changed("value") {
			in.Value = value
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
		result, err := store.RecordMetricSample(ctx, rt.WriteContext(), in)
		if err != nil {
			return rt.EmitError(command, err)
		}
		return rt.Emit(command, result, func(w io.Writer, data any) error {
			fmt.Fprintf(w, "recorded %s = %g %s for %s %s at %s\n",
				in.MetricName, in.Value, in.Unit, in.SubjectType, in.SubjectID, in.SampledAt)
			return nil
		})
	})
	return cmd
}

func metricTrendCmd(opts *GlobalOptions) *cobra.Command {
	var subjectType, subjectID, metricName string
	cmd := &cobra.Command{
		Use:   "trend <subject-id>",
		Short: "Show a metric's observations over time",
		Args:  cobra.MaximumNArgs(1),
	}
	cmd.Flags().StringVar(&subjectType, "subject-type", "", ops.EntityTypeList())
	cmd.Flags().StringVar(&metricName, "name", "", "metric to show")

	cmd.RunE = runArgs(opts, func(rt *Runtime, args []string) int {
		const command = "metric.trend"
		if len(args) == 1 {
			subjectID = args[0]
		}
		store, err := rt.OpsStore(true)
		if err != nil {
			return rt.EmitError(command, err)
		}
		ctx, cancel := rt.Context()
		defer cancel()
		samples, err := store.ListMetricSamples(ctx, subjectType, subjectID, metricName)
		if err != nil {
			return rt.EmitError(command, err)
		}
		return rt.EmitData(command, samples, func(w io.Writer, data any) error {
			list, _ := data.([]ops.MetricSample)
			if len(list) == 0 {
				fmt.Fprintln(w, "(no observations)")
				return nil
			}
			var prev *float64
			for _, m := range list {
				delta := ""
				if prev != nil {
					// State the change; do not interpret it.
					delta = fmt.Sprintf("  (%+g)", m.Value-*prev)
				}
				unit := ""
				if m.Unit != nil {
					unit = " " + *m.Unit
				}
				fmt.Fprintf(w, "%s  %-18s %g%s%s\n", m.SampledAt, m.MetricName, m.Value, unit, delta)
				v := m.Value
				prev = &v
			}
			return nil
		})
	})
	return cmd
}

// newBizCmd groups the read-only business views that have no single owning
// noun. `biz quality` is deliberately separate from `ops status`: one asks
// whether the business record is coherent, the other whether the plan is.
func newBizCmd(opts *GlobalOptions) *cobra.Command {
	cmd := &cobra.Command{Use: "biz", Short: "Cross-cutting business views"}
	cmd.AddCommand(bizQualityCmd(opts))
	return cmd
}

func bizQualityCmd(opts *GlobalOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "quality",
		Short: "Business-record inconsistencies, stated as facts",
		Long: "List business-record inconsistencies.\n\n" +
			"Every row states a fact and none of them is corrected automatically: " +
			"a contract whose instalments do not add up to its declared amount is " +
			"reported here, and the declared amount is left exactly as written. " +
			"Deciding which number is wrong is the user's call, not the system's.",
	}
	cmd.RunE = run(opts, func(rt *Runtime) int {
		const command = "biz.quality"
		store, err := rt.OpsStore(true)
		if err != nil {
			return rt.EmitError(command, err)
		}
		ctx, cancel := rt.Context()
		defer cancel()
		issues, err := store.BizQualityIssues(ctx)
		if err != nil {
			return rt.EmitError(command, err)
		}
		return rt.EmitData(command, issues, func(w io.Writer, data any) error {
			list, _ := data.([]ops.QualityIssue)
			if len(list) == 0 {
				fmt.Fprintln(w, "no business issues")
				return nil
			}
			fmt.Fprintf(w, "%d issue(s)\n", len(list))
			for _, q := range list {
				fmt.Fprintf(w, "  %-38s %-12s %s\n", q.Issue, q.EntityType, q.Title)
			}
			return nil
		})
	})
	return cmd
}
