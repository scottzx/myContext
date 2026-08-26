package cli

import (
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/scottzx/mycontext/internal/ops"
)

// newMilestoneCmd exposes the dated points work aims at. A milestone is not a
// task, so it has its own commands rather than a flag on task.
func newMilestoneCmd(opts *GlobalOptions) *cobra.Command {
	cmd := &cobra.Command{Use: "milestone", Short: "Dated points the work is aiming at"}
	cmd.AddCommand(milestoneCreateCmd(opts), milestoneListCmd(opts), milestoneUpdateCmd(opts))
	return cmd
}

func milestoneCreateCmd(opts *GlobalOptions) *cobra.Command {
	var in ops.CreateMilestoneInput
	var target, current float64

	cmd := &cobra.Command{
		Use:         "create <name>",
		Short:       "Record a dated checkpoint",
		Annotations: map[string]string{"write": "true"},
		Args:        cobra.MaximumNArgs(1),
	}
	cmd.Flags().StringVar(&in.TargetDate, "date", "", "target date, YYYY-MM-DD (required)")
	cmd.Flags().StringVar(&in.ProjectID, "project", "", "project this checkpoint belongs to")
	cmd.Flags().StringVar(&in.KeyResultID, "kr", "", "key result it contributes to")
	cmd.Flags().StringVar(&in.Status, "status", "", "pending|at_risk|hit|missed|cancelled")
	cmd.Flags().StringVar(&in.Importance, "importance", "", "P0|P1|P2|P3")
	cmd.Flags().StringVar(&in.MetricName, "metric", "", "what reaching it measures")
	cmd.Flags().StringVar(&in.MetricUnit, "unit", "", "unit of the metric")
	cmd.Flags().Float64Var(&target, "target", 0, "target value")
	cmd.Flags().Float64Var(&current, "current", 0, "current value")
	cmd.Flags().StringVar(&in.Note, "note", "", "note")

	cmd.RunE = runArgs(opts, func(rt *Runtime, args []string) int {
		const command = "milestone.create"
		if len(args) == 1 {
			in.Name = args[0]
		}
		bindFloat(cmd, "target", &target, &in.TargetValue)
		bindFloat(cmd, "current", &current, &in.CurrentValue)
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
		result, err := store.CreateMilestone(ctx, rt.WriteContext(), in)
		if err != nil {
			return rt.EmitError(command, err)
		}
		return rt.Emit(command, result, func(w io.Writer, data any) error {
			if m, ok := data.(*ops.Milestone); ok {
				fmt.Fprintf(w, "created %s  %s  (%s)\n", m.ID, m.Name, m.TargetDate)
			}
			return nil
		})
	})
	return cmd
}

func milestoneListCmd(opts *GlobalOptions) *cobra.Command {
	var f ops.MilestoneFilter
	cmd := &cobra.Command{Use: "list", Short: "List milestones with the work aimed at them"}
	cmd.Flags().StringVar(&f.ProjectID, "project", "", "filter by project")
	cmd.Flags().StringVar(&f.KeyResultID, "kr", "", "filter by key result")
	cmd.Flags().StringVar(&f.Status, "status", "", "filter by status")
	cmd.Flags().StringVar(&f.Through, "through", "", "only those due on or before YYYY-MM-DD")
	cmd.Flags().BoolVar(&f.OpenOnly, "open", false, "exclude hit and cancelled")
	cmd.Flags().IntVar(&f.Limit, "limit", 0, "maximum rows")

	cmd.RunE = run(opts, func(rt *Runtime) int {
		const command = "milestone.list"
		store, err := rt.OpsStore(true)
		if err != nil {
			return rt.EmitError(command, err)
		}
		ctx, cancel := rt.Context()
		defer cancel()
		items, err := store.ListMilestones(ctx, f)
		if err != nil {
			return rt.EmitError(command, err)
		}
		return rt.EmitData(command, items, func(w io.Writer, data any) error {
			list, ok := data.([]ops.MilestoneProgress)
			if !ok {
				return nil
			}
			if len(list) == 0 {
				fmt.Fprintln(w, "no milestones")
				return nil
			}
			rows := make([][]string, 0, len(list))
			for _, m := range list {
				left := "—"
				if m.DaysLeft != nil {
					left = fmt.Sprintf("%dd", *m.DaysLeft)
				}
				rows = append(rows, []string{
					m.MilestoneID, m.Importance, m.TargetDate, left, m.Status, m.Name,
					fmt.Sprintf("%d/%d", m.DoneCount, m.TaskCount),
					Deref(m.ProjectName),
				})
			}
			return Table(w, []string{"ID", "IMP", "DATE", "LEFT", "STATUS", "NAME", "DONE", "PROJECT"}, rows)
		})
	})
	return cmd
}

func milestoneUpdateCmd(opts *GlobalOptions) *cobra.Command {
	var in ops.UpdateMilestoneInput
	var name, status, importance, date, project, kr, note string
	var target, current float64

	cmd := &cobra.Command{
		Use:         "update <id>",
		Short:       "Patch a milestone (moving its date requires --reason)",
		Annotations: map[string]string{"write": "true"},
		Args:        cobra.ExactArgs(1),
	}
	cmd.Flags().Int64Var(&in.ExpectedVersion, "expected-version", 0, "version read before editing")
	cmd.Flags().StringVar(&name, "name", "", "new name")
	cmd.Flags().StringVar(&status, "status", "", "pending|at_risk|hit|missed|cancelled")
	cmd.Flags().StringVar(&importance, "importance", "", "P0|P1|P2|P3")
	cmd.Flags().StringVar(&date, "date", "", "move the target date (requires --reason)")
	cmd.Flags().StringVar(&project, "project", "", "move to project id")
	cmd.Flags().StringVar(&kr, "kr", "", "key result it contributes to")
	cmd.Flags().StringVar(&note, "note", "", "note")
	cmd.Flags().Float64Var(&target, "target", 0, "target value")
	cmd.Flags().Float64Var(&current, "current", 0, "current value")

	cmd.RunE = runArgs(opts, func(rt *Runtime, args []string) int {
		const command = "milestone.update"
		in.MilestoneID = args[0]
		setIfChanged(cmd, "name", name, &in.Name)
		setIfChanged(cmd, "status", status, &in.Status)
		setIfChanged(cmd, "importance", importance, &in.Importance)
		setIfChanged(cmd, "date", date, &in.TargetDate)
		setIfChanged(cmd, "project", project, &in.ProjectID)
		setIfChanged(cmd, "kr", kr, &in.KeyResultID)
		setIfChanged(cmd, "note", note, &in.Note)
		bindFloat(cmd, "target", &target, &in.TargetValue)
		bindFloat(cmd, "current", &current, &in.CurrentValue)

		store, err := rt.OpsStore(false)
		if err != nil {
			return rt.EmitError(command, err)
		}
		ctx, cancel := rt.Context()
		defer cancel()
		result, err := store.UpdateMilestone(ctx, rt.WriteContext(), in)
		if err != nil {
			return rt.EmitError(command, err)
		}
		return rt.Emit(command, result, func(w io.Writer, data any) error {
			if m, ok := data.(*ops.Milestone); ok {
				fmt.Fprintf(w, "%s  %s  (%s)  [%s]\n", m.ID, m.Name, m.TargetDate, m.Status)
			}
			return nil
		})
	})
	return cmd
}
