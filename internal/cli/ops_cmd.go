package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"github.com/scottzx/mycontext/internal/ops"
)

// ---------------------------------------------------------------------------
// mycontext ops status - the 30-second cockpit
// ---------------------------------------------------------------------------

func newOpsCmd(opts *GlobalOptions) *cobra.Command {
	cmd := &cobra.Command{Use: "ops", Short: "Business execution status"}

	statusCmd := &cobra.Command{
		Use:   "status",
		Short: "Today, tomorrow, the week, overdue, reviews and overload",
		Long: "Reports facts only: counts, planned minutes, available minutes and\n" +
			"overload. It never reorders your priorities or suggests what to drop.",
	}
	statusCmd.RunE = run(opts, func(rt *Runtime) int {
		const command = "ops.status"
		store, err := rt.OpsStore(true)
		if err != nil {
			return rt.EmitError(command, err)
		}
		ctx, cancel := rt.Context()
		defer cancel()

		status, err := store.Status(ctx)
		if err != nil {
			return rt.EmitError(command, err)
		}
		return rt.EmitData(command, status, renderStatus)
	})

	cmd.AddCommand(statusCmd)
	return cmd
}

func renderStatus(w io.Writer, data any) error {
	s, ok := data.(*ops.Status)
	if !ok {
		return fmt.Errorf("unexpected status payload")
	}
	load := s.TodayLoad
	capacityNote := ""
	if load.IsDefaultCapacity {
		capacityNote = " (default capacity)"
	}
	fmt.Fprintf(w, "== %s ==\n", s.Today)
	fmt.Fprintf(w, "capacity: %d planned / %d available min%s",
		load.PlannedMinutes, load.AvailableMinutes, capacityNote)
	if load.OverloadMinutes > 0 {
		fmt.Fprintf(w, "  OVERLOADED by %d min", load.OverloadMinutes)
	}
	fmt.Fprintln(w)
	if load.TasksWithoutEstimate > 0 {
		fmt.Fprintf(w, "note: %d planned task(s) have no estimate, so the total understates the load\n",
			load.TasksWithoutEstimate)
	}

	fmt.Fprintln(w, "\n-- today --")
	if err := renderAgenda(w, s.TodayAgenda); err != nil {
		return err
	}
	fmt.Fprintln(w, "\n-- tomorrow --")
	if err := renderAgenda(w, s.TomorrowAgenda); err != nil {
		return err
	}

	fmt.Fprintln(w, "\n-- next 7 days --")
	weekRows := make([][]string, 0, len(s.Week))
	for _, d := range s.Week {
		flag := ""
		if d.OverloadMinutes > 0 {
			flag = fmt.Sprintf("over by %d", d.OverloadMinutes)
		}
		weekRows = append(weekRows, []string{
			d.Date, fmt.Sprintf("%d", d.TaskCount),
			fmt.Sprintf("%d/%d", d.PlannedMinutes, d.AvailableMinutes), flag,
		})
	}
	if err := Table(w, []string{"DATE", "TASKS", "MIN", "FLAG"}, weekRows); err != nil {
		return err
	}

	if len(s.Overdue) > 0 {
		fmt.Fprintf(w, "\n-- overdue (%d) --\n", s.Totals.Overdue)
		rows := make([][]string, 0, len(s.Overdue))
		for _, o := range s.Overdue {
			rows = append(rows, []string{o.EntityType, o.EntityID, o.Importance, o.Title,
				o.DueAt, fmt.Sprintf("%dd", o.DaysOverdue)})
		}
		if err := Table(w, []string{"WHAT", "ID", "IMP", "TITLE", "DUE", "LATE"}, rows); err != nil {
			return err
		}
	}
	if len(s.Milestones) > 0 {
		fmt.Fprintf(w, "\n-- milestones through this week (%d) --\n", len(s.Milestones))
		rows := make([][]string, 0, len(s.Milestones))
		for _, m := range s.Milestones {
			left := "—"
			if m.DaysLeft != nil {
				left = fmt.Sprintf("%dd", *m.DaysLeft)
			}
			rows = append(rows, []string{m.MilestoneID, m.Importance, m.TargetDate, left,
				m.Name, fmt.Sprintf("%d/%d", m.DoneCount, m.TaskCount), Deref(m.ProjectName)})
		}
		if err := Table(w, []string{"ID", "IMP", "DATE", "LEFT", "NAME", "DONE", "PROJECT"}, rows); err != nil {
			return err
		}
	}
	if len(s.ReviewDue) > 0 {
		fmt.Fprintf(w, "\n-- due for review (%d) --\n", s.Totals.ReviewDue)
		if err := renderAgenda(w, s.ReviewDue); err != nil {
			return err
		}
	}
	if len(s.UnscheduledImportant) > 0 {
		fmt.Fprintf(w, "\n-- important but unscheduled (%d) --\n", s.Totals.UnscheduledImportant)
		if err := renderAgenda(w, s.UnscheduledImportant); err != nil {
			return err
		}
	}
	if s.Totals.Truncated {
		fmt.Fprintf(w, "\nnote: long lists are capped at %d rows; the counts above are the real totals\n",
			ops.ListCap)
	}
	if len(s.QualityIssues) > 0 {
		fmt.Fprintf(w, "\n-- data quality (%d) --\n", s.Totals.QualityIssues)
		rows := make([][]string, 0, len(s.QualityIssues))
		for _, q := range s.QualityIssues {
			rows = append(rows, []string{q.Issue, q.EntityID, q.Title})
		}
		if err := Table(w, []string{"ISSUE", "ID", "TITLE"}, rows); err != nil {
			return err
		}
	}
	return nil
}

func renderAgenda(w io.Writer, entries []ops.AgendaEntry) error {
	rows := make([][]string, 0, len(entries))
	for _, e := range entries {
		rows = append(rows, []string{
			e.Reason, e.EntityID, e.Importance, e.Title,
			Deref(e.ProjectName), DerefInt(e.EffectiveMinutes),
		})
	}
	return Table(w, []string{"WHY", "ID", "IMP", "TITLE", "PROJECT", "MIN"}, rows)
}

// ---------------------------------------------------------------------------
// mycontext task ...
// ---------------------------------------------------------------------------

func newTaskCmd(opts *GlobalOptions) *cobra.Command {
	cmd := &cobra.Command{Use: "task", Short: "Executable actions"}
	cmd.AddCommand(
		taskListCmd(opts),
		taskGetCmd(opts),
		taskCreateCmd(opts),
		taskUpdateCmd(opts),
		taskRescheduleCmd(opts),
		taskCompleteCmd(opts),
		taskSetReviewCmd(opts),
	)
	return cmd
}

func taskListCmd(opts *GlobalOptions) *cobra.Command {
	var f ops.TaskFilter
	var status, importance string
	cmd := &cobra.Command{Use: "list", Short: "List tasks"}
	cmd.Flags().StringVar(&status, "status", "", "comma-separated statuses")
	cmd.Flags().StringVar(&importance, "importance", "", "comma-separated P0..P3")
	cmd.Flags().StringVar(&f.ProjectID, "project", "", "project id")
	cmd.Flags().StringVar(&f.Search, "search", "", "match title or detail")
	cmd.Flags().StringVar(&f.PlannedDate, "date", "", "planned for this YYYY-MM-DD")
	cmd.Flags().BoolVar(&f.Open, "open", false, "exclude done/cancelled/archived")
	cmd.Flags().BoolVar(&f.Unscheduled, "unscheduled", false, "only tasks with no active plan")
	cmd.Flags().IntVar(&f.Limit, "limit", 100, "maximum rows")
	cmd.Flags().IntVar(&f.Offset, "offset", 0, "rows to skip")

	cmd.RunE = run(opts, func(rt *Runtime) int {
		const command = "task.list"
		f.Status = splitList(status)
		f.Importance = splitList(importance)

		store, err := rt.OpsStore(true)
		if err != nil {
			return rt.EmitError(command, err)
		}
		ctx, cancel := rt.Context()
		defer cancel()

		tasks, err := store.ListTasks(ctx, f)
		if err != nil {
			return rt.EmitError(command, err)
		}
		return rt.EmitData(command, tasks, renderTaskList)
	})
	return cmd
}

func renderTaskList(w io.Writer, data any) error {
	tasks, ok := data.([]*ops.Task)
	if !ok {
		return fmt.Errorf("unexpected task list payload")
	}
	rows := make([][]string, 0, len(tasks))
	for _, t := range tasks {
		planned := "-"
		if t.Schedule != nil {
			planned = t.Schedule.PlannedDate
		}
		rows = append(rows, []string{
			t.ID, string(t.Importance), string(t.Status), planned,
			Deref(t.HardDueAt), DerefInt(t.EstimateMinutes),
			fmt.Sprintf("v%d", t.Version), t.Title,
		})
	}
	return Table(w, []string{"ID", "IMP", "STATUS", "PLANNED", "HARD DUE", "MIN", "VER", "TITLE"}, rows)
}

func taskGetCmd(opts *GlobalOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "get <id|search>",
		Short: "Show one task; a search matching several returns the candidates",
		Args:  cobra.ExactArgs(1),
	}
	cmd.RunE = runArgs(opts, func(rt *Runtime, args []string) int {
		const command = "task.get"
		store, err := rt.OpsStore(true)
		if err != nil {
			return rt.EmitError(command, err)
		}
		ctx, cancel := rt.Context()
		defer cancel()

		task, err := store.FindTaskByReference(ctx, args[0])
		if err != nil {
			return rt.EmitError(command, err)
		}
		return rt.EmitData(command, task, func(w io.Writer, _ any) error {
			return renderTaskDetail(w, task)
		})
	})
	return cmd
}

func renderTaskDetail(w io.Writer, t *ops.Task) error {
	fmt.Fprintf(w, "%s  [%s %s]  v%d\n", t.Title, t.Importance, t.Status, t.Version)
	fmt.Fprintf(w, "id:            %s\n", t.ID)
	fmt.Fprintf(w, "project:       %s\n", Deref(t.ProjectID))
	fmt.Fprintf(w, "hard due:      %s\n", Deref(t.HardDueAt))
	fmt.Fprintf(w, "next review:   %s\n", Deref(t.NextReviewAt))
	fmt.Fprintf(w, "estimate:      %s min\n", DerefInt(t.EstimateMinutes))
	fmt.Fprintf(w, "waiting for:   %s\n", Deref(t.WaitingFor))
	if t.Schedule != nil {
		fmt.Fprintf(w, "planned:       %s %s\n", t.Schedule.PlannedDate, Deref(t.Schedule.TimeSlot))
	} else {
		fmt.Fprintf(w, "planned:       -\n")
	}
	if t.CompletionCriteria != nil {
		fmt.Fprintf(w, "done when:     %s\n", *t.CompletionCriteria)
	}
	if t.Detail != nil {
		fmt.Fprintf(w, "\n%s\n", *t.Detail)
	}
	return nil
}

func taskCreateCmd(opts *GlobalOptions) *cobra.Command {
	var in ops.CreateTaskInput
	cmd := &cobra.Command{
		Use:         "create <title>",
		Short:       "Create a task",
		Annotations: map[string]string{"write": "true"},
		Args:        cobra.MaximumNArgs(1),
	}
	cmd.Flags().StringVar(&in.ProjectID, "project", "", "project id (a sprint is a project)")
	cmd.Flags().StringVar(&in.ParentTaskID, "parent", "", "parent task id")
	cmd.Flags().StringVar(&in.MilestoneID, "milestone", "", "milestone this task works towards")
	cmd.Flags().StringVar(&in.MetricName, "metric", "", "what this task contributes to")
	cmd.Flags().StringVar(&in.MetricUnit, "unit", "", "unit of the metric")
	cmd.Flags().StringVar(&in.EarliestStartAt, "earliest-start", "", "not before this instant, RFC 3339")
	cmd.Flags().StringVar(&in.Detail, "detail", "", "longer description")
	cmd.Flags().StringVar(&in.CompletionCriteria, "done-when", "", "completion criteria")
	cmd.Flags().StringVar(&in.Status, "status", "", "inbox|todo|doing|waiting")
	cmd.Flags().StringVar(&in.Importance, "importance", "", "P0|P1|P2|P3")
	cmd.Flags().StringVar(&in.HardDueAt, "hard-due", "", "real deadline, RFC 3339")
	cmd.Flags().StringVar(&in.NextReviewAt, "review", "", "review date, YYYY-MM-DD")
	cmd.Flags().IntVar(&in.EstimateMinutes, "estimate", 0, "estimated minutes")
	cmd.Flags().StringVar(&in.WaitingFor, "waiting-for", "", "person or condition being waited on")
	cmd.Flags().StringVar(&in.PlannedDate, "plan", "", "planned date, YYYY-MM-DD")
	cmd.Flags().StringVar(&in.TimeSlot, "slot", "", "morning|afternoon|evening")
	cmd.Flags().IntVar(&in.PlannedMinutes, "plan-minutes", 0, "minutes reserved on the planned day")

	cmd.RunE = runArgs(opts, func(rt *Runtime, args []string) int {
		const command = "task.create"
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

		result, err := store.CreateTask(ctx, rt.WriteContext(), in)
		if err != nil {
			return rt.EmitError(command, err)
		}
		return rt.Emit(command, result, func(w io.Writer, data any) error {
			if t, ok := data.(*ops.Task); ok {
				fmt.Fprintf(w, "created %s\n", t.ID)
				return renderTaskDetail(w, t)
			}
			return nil
		})
	})
	return cmd
}

func taskUpdateCmd(opts *GlobalOptions) *cobra.Command {
	var in ops.UpdateTaskInput
	var title, detail, status, importance, waitingFor, project, milestone, hardDue, review, doneWhen string
	var estimate int

	cmd := &cobra.Command{
		Use:         "update <id>",
		Short:       "Patch a task (requires --expected-version)",
		Annotations: map[string]string{"write": "true"},
		Args:        cobra.ExactArgs(1),
	}
	cmd.Flags().Int64Var(&in.ExpectedVersion, "expected-version", 0, "version read before editing")
	cmd.Flags().StringVar(&title, "title", "", "new title")
	cmd.Flags().StringVar(&detail, "detail", "", "new detail")
	cmd.Flags().StringVar(&status, "status", "", "new status")
	cmd.Flags().StringVar(&importance, "importance", "", "P0|P1|P2|P3")
	cmd.Flags().StringVar(&waitingFor, "waiting-for", "", "person or condition")
	cmd.Flags().StringVar(&project, "project", "", "move to project id")
	cmd.Flags().StringVar(&milestone, "milestone", "", "milestone this task works towards (empty to detach)")
	cmd.Flags().StringVar(&hardDue, "hard-due", "", "change the real deadline (requires --reason)")
	cmd.Flags().StringVar(&review, "review", "", "review date, YYYY-MM-DD")
	cmd.Flags().StringVar(&doneWhen, "done-when", "", "completion criteria")
	cmd.Flags().IntVar(&estimate, "estimate", -1, "estimated minutes")

	cmd.RunE = runArgs(opts, func(rt *Runtime, args []string) int {
		const command = "task.update"
		in.TaskID = args[0]
		// Only flags the user actually typed become part of the patch, so an
		// unset flag can never clear a field.
		setIfChanged(cmd, "title", title, &in.Title)
		setIfChanged(cmd, "detail", detail, &in.Detail)
		setIfChanged(cmd, "status", status, &in.Status)
		setIfChanged(cmd, "importance", importance, &in.Importance)
		setIfChanged(cmd, "waiting-for", waitingFor, &in.WaitingFor)
		setIfChanged(cmd, "project", project, &in.ProjectID)
		setIfChanged(cmd, "milestone", milestone, &in.MilestoneID)
		setIfChanged(cmd, "hard-due", hardDue, &in.HardDueAt)
		setIfChanged(cmd, "review", review, &in.NextReviewAt)
		setIfChanged(cmd, "done-when", doneWhen, &in.CompletionCrit)
		if cmd.Flags().Changed("estimate") {
			in.EstimateMinutes = &estimate
		}

		store, err := rt.OpsStore(false)
		if err != nil {
			return rt.EmitError(command, err)
		}
		ctx, cancel := rt.Context()
		defer cancel()

		result, err := store.UpdateTask(ctx, rt.WriteContext(), in)
		if err != nil {
			return rt.EmitError(command, err)
		}
		return rt.Emit(command, result, func(w io.Writer, data any) error {
			if t, ok := data.(*ops.Task); ok {
				return renderTaskDetail(w, t)
			}
			return nil
		})
	})
	return cmd
}

func taskRescheduleCmd(opts *GlobalOptions) *cobra.Command {
	var in ops.RescheduleInput
	cmd := &cobra.Command{
		Use:         "reschedule <id> <YYYY-MM-DD>",
		Short:       "Move a task to another day, keeping the old plan in history",
		Annotations: map[string]string{"write": "true"},
		Args:        cobra.ExactArgs(2),
	}
	cmd.Flags().Int64Var(&in.ExpectedVersion, "expected-version", 0, "version read before editing")
	cmd.Flags().StringVar(&in.TimeSlot, "slot", "", "morning|afternoon|evening")
	cmd.Flags().IntVar(&in.PlannedMinutes, "plan-minutes", 0, "minutes reserved on the new day")
	cmd.Flags().StringVar(&in.Note, "note", "", "note stored on the new schedule")

	cmd.RunE = runArgs(opts, func(rt *Runtime, args []string) int {
		const command = "task.reschedule"
		in.TaskID, in.NewDate = args[0], args[1]

		store, err := rt.OpsStore(false)
		if err != nil {
			return rt.EmitError(command, err)
		}
		ctx, cancel := rt.Context()
		defer cancel()

		// A human at a terminal should not have to look the version up first;
		// an agent must pass it so two writers cannot silently overwrite.
		if in.ExpectedVersion == 0 {
			if err := fillExpectedVersion(ctx, rt, store, in.TaskID, &in.ExpectedVersion); err != nil {
				return rt.EmitError(command, err)
			}
		}
		result, err := store.RescheduleTask(ctx, rt.WriteContext(), in)
		if err != nil {
			return rt.EmitError(command, err)
		}
		return rt.Emit(command, result, func(w io.Writer, data any) error {
			if t, ok := data.(*ops.Task); ok {
				fmt.Fprintf(w, "%s moved to %s\n", t.ID, in.NewDate)
				return renderTaskDetail(w, t)
			}
			return nil
		})
	})
	return cmd
}

func taskCompleteCmd(opts *GlobalOptions) *cobra.Command {
	var in ops.CompleteTaskInput
	cmd := &cobra.Command{
		Use:         "complete <id>",
		Short:       "Mark a task done",
		Annotations: map[string]string{"write": "true"},
		Args:        cobra.ExactArgs(1),
	}
	cmd.Flags().Int64Var(&in.ExpectedVersion, "expected-version", 0, "version read before editing")
	cmd.Flags().StringVar(&in.Note, "note", "", "closing note")

	cmd.RunE = runArgs(opts, func(rt *Runtime, args []string) int {
		const command = "task.complete"
		in.TaskID = args[0]

		store, err := rt.OpsStore(false)
		if err != nil {
			return rt.EmitError(command, err)
		}
		ctx, cancel := rt.Context()
		defer cancel()

		if in.ExpectedVersion == 0 {
			if err := fillExpectedVersion(ctx, rt, store, in.TaskID, &in.ExpectedVersion); err != nil {
				return rt.EmitError(command, err)
			}
		}
		result, err := store.CompleteTask(ctx, rt.WriteContext(), in)
		if err != nil {
			return rt.EmitError(command, err)
		}
		return rt.Emit(command, result, func(w io.Writer, data any) error {
			if t, ok := data.(*ops.Task); ok {
				fmt.Fprintf(w, "completed %s\n", t.ID)
			}
			return nil
		})
	})
	return cmd
}

func taskSetReviewCmd(opts *GlobalOptions) *cobra.Command {
	var in ops.SetReviewInput
	cmd := &cobra.Command{
		Use:         "set-review <id> <YYYY-MM-DD>",
		Short:       "Park a task until a date; it leaves today but never disappears",
		Annotations: map[string]string{"write": "true"},
		Args:        cobra.ExactArgs(2),
	}
	cmd.Flags().Int64Var(&in.ExpectedVersion, "expected-version", 0, "version read before editing")
	cmd.Flags().StringVar(&in.Status, "status", "", "new status, e.g. waiting")
	cmd.Flags().StringVar(&in.WaitingFor, "waiting-for", "", "person or condition")

	cmd.RunE = runArgs(opts, func(rt *Runtime, args []string) int {
		const command = "task.set-review"
		in.TaskID, in.ReviewDate = args[0], args[1]

		store, err := rt.OpsStore(false)
		if err != nil {
			return rt.EmitError(command, err)
		}
		ctx, cancel := rt.Context()
		defer cancel()

		if in.ExpectedVersion == 0 {
			if err := fillExpectedVersion(ctx, rt, store, in.TaskID, &in.ExpectedVersion); err != nil {
				return rt.EmitError(command, err)
			}
		}
		result, err := store.SetTaskReview(ctx, rt.WriteContext(), in)
		if err != nil {
			return rt.EmitError(command, err)
		}
		return rt.Emit(command, result, func(w io.Writer, data any) error {
			if t, ok := data.(*ops.Task); ok {
				fmt.Fprintf(w, "%s will resurface on %s\n", t.ID, in.ReviewDate)
			}
			return nil
		})
	})
	return cmd
}

func splitList(value string) []string {
	if value == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if trimmed := strings.TrimSpace(p); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// setIfChanged copies a flag value into an optional patch field only when the
// user actually passed the flag.
func setIfChanged(cmd *cobra.Command, name, value string, target **string) {
	if cmd.Flags().Changed(name) {
		v := value
		*target = &v
	}
}
