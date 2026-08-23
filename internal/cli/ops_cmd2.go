package cli

import (
	"context"
	"fmt"
	"io"

	"github.com/spf13/cobra"

	"github.com/scottzx/mycontext/internal/ops"
	"github.com/scottzx/mycontext/internal/protocol"
)

// fillExpectedVersion reads the current version for an interactive caller.
// Agents are expected to pass --expected-version themselves; this convenience
// exists so a person at a terminal is not forced into a read-then-write dance.
func fillExpectedVersion(ctx context.Context, rt *Runtime, store *ops.Store, taskID string, target *int64) error {
	if rt.Opts.Actor == "agent" {
		// An agent that skipped the read has no basis for a safe write.
		return protocol.BadInput("expected_version is required when --actor=agent")
	}
	task, err := store.GetTask(ctx, taskID)
	if err != nil {
		return err
	}
	*target = task.Version
	return nil
}

// ---------------------------------------------------------------------------
// mycontext project ...
// ---------------------------------------------------------------------------

func newProjectCmd(opts *GlobalOptions) *cobra.Command {
	cmd := &cobra.Command{Use: "project", Short: "Bounded work with a defined outcome"}
	cmd.AddCommand(
		projectListCmd(opts),
		projectGetCmd(opts),
		projectCreateCmd(opts),
		projectUpdateCmd(opts),
		projectLinkKRCmd(opts),
		projectTreeCmd(opts),
	)
	return cmd
}

func projectListCmd(opts *GlobalOptions) *cobra.Command {
	var f ops.ProjectFilter
	var status string
	cmd := &cobra.Command{Use: "list", Short: "List projects with open task counts"}
	cmd.Flags().StringVar(&status, "status", "", "comma-separated statuses")
	cmd.Flags().StringVar(&f.InitiativeID, "initiative", "", "initiative id")
	cmd.Flags().StringVar(&f.AreaID, "area", "", "area id")
	cmd.Flags().StringVar(&f.Search, "search", "", "match name")
	cmd.Flags().BoolVar(&f.Open, "open", false, "only planned/active/waiting/paused")
	cmd.Flags().IntVar(&f.Limit, "limit", 200, "maximum rows")

	cmd.RunE = run(opts, func(rt *Runtime) int {
		const command = "project.list"
		f.Status = splitList(status)
		store, err := rt.OpsStore(true)
		if err != nil {
			return rt.EmitError(command, err)
		}
		ctx, cancel := rt.Context()
		defer cancel()

		projects, err := store.ListProjects(ctx, f)
		if err != nil {
			return rt.EmitError(command, err)
		}
		return rt.EmitData(command, projects, func(w io.Writer, _ any) error {
			rows := make([][]string, 0, len(projects))
			for _, p := range projects {
				next := Deref(p.NextPlannedDate)
				flag := ""
				if p.Status == "active" && p.OpenTasks == 0 {
					flag = "no next action"
				}
				rows = append(rows, []string{
					p.ID, string(p.Importance), string(p.Status),
					fmt.Sprintf("%d", p.OpenTasks), next, p.Name, flag,
				})
			}
			return Table(w, []string{"ID", "IMP", "STATUS", "OPEN", "NEXT", "NAME", "FLAG"}, rows)
		})
	})
	return cmd
}

func projectGetCmd(opts *GlobalOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "get <id|search>",
		Short: "Show one project and its open tasks",
		Args:  cobra.ExactArgs(1),
	}
	cmd.RunE = runArgs(opts, func(rt *Runtime, args []string) int {
		const command = "project.get"
		store, err := rt.OpsStore(true)
		if err != nil {
			return rt.EmitError(command, err)
		}
		ctx, cancel := rt.Context()
		defer cancel()

		project, err := store.FindProjectByReference(ctx, args[0])
		if err != nil {
			return rt.EmitError(command, err)
		}
		tasks, err := store.ListTasks(ctx, ops.TaskFilter{ProjectID: project.ID, Open: true})
		if err != nil {
			return rt.EmitError(command, err)
		}
		return rt.EmitData(command, map[string]any{"project": project, "open_tasks": tasks},
			func(w io.Writer, _ any) error {
				fmt.Fprintf(w, "%s  [%s %s]  v%d\n", project.Name, project.Importance, project.Status, project.Version)
				fmt.Fprintf(w, "id:          %s\n", project.ID)
				fmt.Fprintf(w, "stage:       %s\n", Deref(project.Stage))
				fmt.Fprintf(w, "target date: %s\n", Deref(project.TargetDate))
				fmt.Fprintf(w, "hard due:    %s\n", Deref(project.HardDueAt))
				fmt.Fprintf(w, "next review: %s\n", Deref(project.NextReviewAt))
				fmt.Fprintf(w, "outcome:     %s\n", Deref(project.Outcome))
				fmt.Fprintf(w, "done when:   %s\n\n", Deref(project.CompletionCriteria))
				return renderTaskList(w, tasks)
			})
	})
	return cmd
}

func projectCreateCmd(opts *GlobalOptions) *cobra.Command {
	var in ops.CreateProjectInput
	cmd := &cobra.Command{
		Use:         "create <name>",
		Short:       "Create a project",
		Annotations: map[string]string{"write": "true"},
		Args:        cobra.MaximumNArgs(1),
	}
	cmd.Flags().StringVar(&in.InitiativeID, "initiative", "", "initiative id")
	cmd.Flags().StringVar(&in.Description, "description", "", "description")
	cmd.Flags().StringVar(&in.Status, "status", "", "planned|active|waiting|paused")
	cmd.Flags().StringVar(&in.Stage, "stage", "", "discover|plan|build|deliver|operate|close")
	cmd.Flags().StringVar(&in.Importance, "importance", "", "P0|P1|P2|P3")
	cmd.Flags().StringVar(&in.TargetDate, "target-date", "", "intended completion, YYYY-MM-DD")
	cmd.Flags().StringVar(&in.HardDueAt, "hard-due", "", "external deadline, RFC 3339")
	cmd.Flags().StringVar(&in.NextReviewAt, "review", "", "review date, YYYY-MM-DD")
	cmd.Flags().StringVar(&in.Outcome, "outcome", "", "what this produces")
	cmd.Flags().StringVar(&in.CompletionCriteria, "done-when", "", "completion criteria")

	cmd.RunE = runArgs(opts, func(rt *Runtime, args []string) int {
		const command = "project.create"
		if len(args) == 1 {
			in.Name = args[0]
		}
		store, err := rt.OpsStore(false)
		if err != nil {
			return rt.EmitError(command, err)
		}
		ctx, cancel := rt.Context()
		defer cancel()

		result, err := store.CreateProject(ctx, rt.WriteContext(), in)
		if err != nil {
			return rt.EmitError(command, err)
		}
		return rt.Emit(command, result, func(w io.Writer, data any) error {
			if p, ok := data.(*ops.Project); ok {
				fmt.Fprintf(w, "created %s  %s\n", p.ID, p.Name)
			}
			return nil
		})
	})
	return cmd
}

func projectUpdateCmd(opts *GlobalOptions) *cobra.Command {
	var in ops.UpdateProjectInput
	var name, description, status, stage, importance, targetDate, hardDue, review, outcome, doneWhen, initiative string

	cmd := &cobra.Command{
		Use:         "update <id>",
		Short:       "Patch a project (requires --expected-version)",
		Annotations: map[string]string{"write": "true"},
		Args:        cobra.ExactArgs(1),
	}
	cmd.Flags().Int64Var(&in.ExpectedVersion, "expected-version", 0, "version read before editing")
	cmd.Flags().StringVar(&name, "name", "", "new name")
	cmd.Flags().StringVar(&description, "description", "", "new description")
	cmd.Flags().StringVar(&status, "status", "", "new status")
	cmd.Flags().StringVar(&stage, "stage", "", "new stage")
	cmd.Flags().StringVar(&importance, "importance", "", "P0|P1|P2|P3")
	cmd.Flags().StringVar(&targetDate, "target-date", "", "intended completion")
	cmd.Flags().StringVar(&hardDue, "hard-due", "", "external deadline (requires --reason)")
	cmd.Flags().StringVar(&review, "review", "", "review date")
	cmd.Flags().StringVar(&outcome, "outcome", "", "what this produces")
	cmd.Flags().StringVar(&doneWhen, "done-when", "", "completion criteria")
	cmd.Flags().StringVar(&initiative, "initiative", "", "move to initiative id")

	cmd.RunE = runArgs(opts, func(rt *Runtime, args []string) int {
		const command = "project.update"
		in.ProjectID = args[0]
		setIfChanged(cmd, "name", name, &in.Name)
		setIfChanged(cmd, "description", description, &in.Description)
		setIfChanged(cmd, "status", status, &in.Status)
		setIfChanged(cmd, "stage", stage, &in.Stage)
		setIfChanged(cmd, "importance", importance, &in.Importance)
		setIfChanged(cmd, "target-date", targetDate, &in.TargetDate)
		setIfChanged(cmd, "hard-due", hardDue, &in.HardDueAt)
		setIfChanged(cmd, "review", review, &in.NextReviewAt)
		setIfChanged(cmd, "outcome", outcome, &in.Outcome)
		setIfChanged(cmd, "done-when", doneWhen, &in.CompletionCriteria)
		setIfChanged(cmd, "initiative", initiative, &in.InitiativeID)

		store, err := rt.OpsStore(false)
		if err != nil {
			return rt.EmitError(command, err)
		}
		ctx, cancel := rt.Context()
		defer cancel()

		if in.ExpectedVersion == 0 && rt.Opts.Actor != "agent" {
			project, err := store.GetProject(ctx, in.ProjectID)
			if err != nil {
				return rt.EmitError(command, err)
			}
			in.ExpectedVersion = project.Version
		}
		result, err := store.UpdateProject(ctx, rt.WriteContext(), in)
		if err != nil {
			return rt.EmitError(command, err)
		}
		return rt.Emit(command, result, func(w io.Writer, data any) error {
			if p, ok := data.(*ops.Project); ok {
				fmt.Fprintf(w, "%s  [%s %s]  v%d\n", p.Name, p.Importance, p.Status, p.Version)
			}
			return nil
		})
	})
	return cmd
}

func projectLinkKRCmd(opts *GlobalOptions) *cobra.Command {
	var in ops.LinkProjectKRInput
	cmd := &cobra.Command{
		Use:         "link-kr <project-id> <key-result-id>",
		Short:       "Record that a project contributes to a key result",
		Annotations: map[string]string{"write": "true"},
		Args:        cobra.ExactArgs(2),
	}
	cmd.Flags().StringVar(&in.Note, "note", "", "how it contributes")
	cmd.RunE = runArgs(opts, func(rt *Runtime, args []string) int {
		const command = "project.link-kr"
		in.ProjectID, in.KeyResultID = args[0], args[1]
		store, err := rt.OpsStore(false)
		if err != nil {
			return rt.EmitError(command, err)
		}
		ctx, cancel := rt.Context()
		defer cancel()

		result, err := store.LinkProjectKR(ctx, rt.WriteContext(), in)
		if err != nil {
			return rt.EmitError(command, err)
		}
		return rt.Emit(command, result, func(w io.Writer, _ any) error {
			fmt.Fprintf(w, "linked %s -> %s\n", in.ProjectID, in.KeyResultID)
			return nil
		})
	})
	return cmd
}

func projectTreeCmd(opts *GlobalOptions) *cobra.Command {
	var includeArchived bool
	cmd := &cobra.Command{Use: "tree", Short: "Area > Initiative > Project navigation"}
	cmd.Flags().BoolVar(&includeArchived, "all", false, "include archived areas and initiatives")

	cmd.RunE = run(opts, func(rt *Runtime) int {
		const command = "project.tree"
		store, err := rt.OpsStore(true)
		if err != nil {
			return rt.EmitError(command, err)
		}
		ctx, cancel := rt.Context()
		defer cancel()

		tree, err := store.Tree(ctx, includeArchived)
		if err != nil {
			return rt.EmitError(command, err)
		}
		return rt.EmitData(command, tree, func(w io.Writer, _ any) error {
			for _, area := range tree {
				fmt.Fprintf(w, "%s  (%s)\n", area.Area.Name, area.Area.ID)
				for _, init := range area.Initiatives {
					fmt.Fprintf(w, "  └ %s  [%s]  (%s)\n", init.Initiative.Name,
						init.Initiative.Status, init.Initiative.ID)
					for _, p := range init.Projects {
						fmt.Fprintf(w, "      └ %s  [%s %s]  %d open  (%s)\n",
							p.Name, p.Importance, p.Status, p.OpenTasks, p.ID)
					}
				}
			}
			return nil
		})
	})
	return cmd
}

// ---------------------------------------------------------------------------
// mycontext area / initiative
// ---------------------------------------------------------------------------

func newAreaCmd(opts *GlobalOptions) *cobra.Command {
	cmd := &cobra.Command{Use: "area", Short: "Long-lived domains of work"}
	var in ops.CreateAreaInput
	createCmd := &cobra.Command{Use: "create <name>", Short: "Create an area", Args: cobra.ExactArgs(1),
		Annotations: map[string]string{"write": "true"}}
	createCmd.Flags().IntVar(&in.SortOrder, "sort", 0, "display order")
	createCmd.Flags().StringVar(&in.Note, "note", "", "note")
	createCmd.RunE = runArgs(opts, func(rt *Runtime, args []string) int {
		const command = "area.create"
		in.Name = args[0]
		store, err := rt.OpsStore(false)
		if err != nil {
			return rt.EmitError(command, err)
		}
		ctx, cancel := rt.Context()
		defer cancel()
		result, err := store.CreateArea(ctx, rt.WriteContext(), in)
		if err != nil {
			return rt.EmitError(command, err)
		}
		return rt.Emit(command, result, func(w io.Writer, data any) error {
			if a, ok := data.(ops.Area); ok {
				fmt.Fprintf(w, "created %s  %s\n", a.ID, a.Name)
			}
			return nil
		})
	})
	cmd.AddCommand(createCmd)
	return cmd
}

func newInitiativeCmd(opts *GlobalOptions) *cobra.Command {
	cmd := &cobra.Command{Use: "initiative", Short: "Directions inside an area"}
	var in ops.CreateInitiativeInput
	createCmd := &cobra.Command{Use: "create <name>", Short: "Create an initiative", Args: cobra.ExactArgs(1),
		Annotations: map[string]string{"write": "true"}}
	createCmd.Flags().StringVar(&in.AreaID, "area", "", "area id (required)")
	createCmd.Flags().StringVar(&in.Description, "description", "", "description")
	createCmd.Flags().StringVar(&in.StartDate, "start", "", "start date, YYYY-MM-DD")
	createCmd.Flags().StringVar(&in.ReviewDate, "review", "", "review date, YYYY-MM-DD")
	createCmd.Flags().IntVar(&in.SortOrder, "sort", 0, "display order")
	createCmd.RunE = runArgs(opts, func(rt *Runtime, args []string) int {
		const command = "initiative.create"
		in.Name = args[0]
		store, err := rt.OpsStore(false)
		if err != nil {
			return rt.EmitError(command, err)
		}
		ctx, cancel := rt.Context()
		defer cancel()
		result, err := store.CreateInitiative(ctx, rt.WriteContext(), in)
		if err != nil {
			return rt.EmitError(command, err)
		}
		return rt.Emit(command, result, func(w io.Writer, data any) error {
			if i, ok := data.(ops.Initiative); ok {
				fmt.Fprintf(w, "created %s  %s\n", i.ID, i.Name)
			}
			return nil
		})
	})
	cmd.AddCommand(createCmd)
	return cmd
}

// ---------------------------------------------------------------------------
// mycontext schedule day|week
// ---------------------------------------------------------------------------

func newScheduleCmd(opts *GlobalOptions) *cobra.Command {
	cmd := &cobra.Command{Use: "schedule", Short: "Day and week boards"}

	dayCmd := &cobra.Command{
		Use:   "day [YYYY-MM-DD]",
		Short: "One day's agenda and load",
		Args:  cobra.MaximumNArgs(1),
	}
	dayCmd.RunE = runArgs(opts, func(rt *Runtime, args []string) int {
		const command = "schedule.day"
		store, err := rt.OpsStore(true)
		if err != nil {
			return rt.EmitError(command, err)
		}
		ctx, cancel := rt.Context()
		defer cancel()

		date := rt.Clock.Today()
		if len(args) == 1 {
			date = args[0]
		}
		day, err := store.Day(ctx, date)
		if err != nil {
			return rt.EmitError(command, err)
		}
		return rt.EmitData(command, day, func(w io.Writer, _ any) error {
			return renderDay(w, *day)
		})
	})

	weekCmd := &cobra.Command{
		Use:   "week [YYYY-MM-DD]",
		Short: "Seven days from a start date (default today)",
		Args:  cobra.MaximumNArgs(1),
	}
	weekCmd.RunE = runArgs(opts, func(rt *Runtime, args []string) int {
		const command = "schedule.week"
		store, err := rt.OpsStore(true)
		if err != nil {
			return rt.EmitError(command, err)
		}
		ctx, cancel := rt.Context()
		defer cancel()

		start := ""
		if len(args) == 1 {
			start = args[0]
		}
		week, err := store.Week(ctx, start)
		if err != nil {
			return rt.EmitError(command, err)
		}
		return rt.EmitData(command, week, func(w io.Writer, _ any) error {
			for i, day := range week {
				if i > 0 {
					fmt.Fprintln(w)
				}
				if err := renderDay(w, day); err != nil {
					return err
				}
			}
			return nil
		})
	})

	cmd.AddCommand(dayCmd, weekCmd)
	return cmd
}

func renderDay(w io.Writer, day ops.DayView) error {
	note := ""
	if day.Load.IsDefaultCapacity {
		note = " (default capacity)"
	}
	over := ""
	if day.Load.OverloadMinutes > 0 {
		over = fmt.Sprintf("  OVERLOADED by %d min", day.Load.OverloadMinutes)
	}
	fmt.Fprintf(w, "%s  %d task(s)  %d/%d min%s%s\n", day.Load.Date, day.Load.TaskCount,
		day.Load.PlannedMinutes, day.Load.AvailableMinutes, note, over)
	return renderAgenda(w, day.Entries)
}

// ---------------------------------------------------------------------------
// mycontext capacity set
// ---------------------------------------------------------------------------

func newCapacityCmd(opts *GlobalOptions) *cobra.Command {
	cmd := &cobra.Command{Use: "capacity", Short: "Declared available minutes per day"}
	var in ops.SetCapacityInput
	setCmd := &cobra.Command{
		Use:         "set <YYYY-MM-DD> <minutes>",
		Short:       "Declare how many minutes a day actually has",
		Annotations: map[string]string{"write": "true"},
		Args:        cobra.ExactArgs(2),
	}
	setCmd.Flags().StringVar(&in.Note, "note", "", "why")
	setCmd.RunE = runArgs(opts, func(rt *Runtime, args []string) int {
		const command = "capacity.set"
		in.Date = args[0]
		if _, err := fmt.Sscanf(args[1], "%d", &in.AvailableMinutes); err != nil {
			return rt.EmitError(command, protocol.BadInput("minutes must be an integer, got %q", args[1]))
		}
		store, err := rt.OpsStore(false)
		if err != nil {
			return rt.EmitError(command, err)
		}
		ctx, cancel := rt.Context()
		defer cancel()

		result, err := store.SetCapacity(ctx, rt.WriteContext(), in)
		if err != nil {
			return rt.EmitError(command, err)
		}
		return rt.Emit(command, result, func(w io.Writer, _ any) error {
			fmt.Fprintf(w, "%s: %d available minutes\n", in.Date, in.AvailableMinutes)
			return nil
		})
	})
	cmd.AddCommand(setCmd)
	return cmd
}

// ---------------------------------------------------------------------------
// mycontext event list
// ---------------------------------------------------------------------------

func newEventCmd(opts *GlobalOptions) *cobra.Command {
	var f ops.EventFilter
	cmd := &cobra.Command{Use: "event", Short: "Audit trail"}
	listCmd := &cobra.Command{Use: "list", Short: "List events, newest first"}
	listCmd.Flags().StringVar(&f.EntityType, "entity-type", "", "task|project|area|initiative|capacity")
	listCmd.Flags().StringVar(&f.EntityID, "entity-id", "", "specific object")
	listCmd.Flags().StringVar(&f.EventType, "event-type", "", "created|rescheduled|status_changed|...")
	listCmd.Flags().StringVar(&f.Since, "since", "", "RFC 3339 lower bound")
	listCmd.Flags().IntVar(&f.Limit, "limit", 50, "maximum rows")

	listCmd.RunE = run(opts, func(rt *Runtime) int {
		const command = "event.list"
		store, err := rt.OpsStore(true)
		if err != nil {
			return rt.EmitError(command, err)
		}
		ctx, cancel := rt.Context()
		defer cancel()

		events, err := store.ListEvents(ctx, f)
		if err != nil {
			return rt.EmitError(command, err)
		}
		return rt.EmitData(command, events, func(w io.Writer, _ any) error {
			rows := make([][]string, 0, len(events))
			for _, e := range events {
				rows = append(rows, []string{
					e.OccurredAt, e.EntityType, e.EntityID, e.EventType,
					e.ActorType, Deref(e.Reason),
				})
			}
			return Table(w, []string{"WHEN", "TYPE", "ID", "EVENT", "ACTOR", "REASON"}, rows)
		})
	})
	cmd.AddCommand(listCmd)
	return cmd
}
