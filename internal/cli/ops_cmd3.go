package cli

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/scottzx/mycontext/internal/ops"
)

// newObjectiveCmd exposes the outcome system, which until now had tables but
// no door: nothing in the CLI could create an objective or a key result.
func newObjectiveCmd(opts *GlobalOptions) *cobra.Command {
	cmd := &cobra.Command{Use: "objective", Short: "Directions with a horizon"}

	var in ops.CreateObjectiveInput
	createCmd := &cobra.Command{
		Use:         "create <name>",
		Short:       "Create an objective",
		Annotations: map[string]string{"write": "true"},
		Args:        cobra.MaximumNArgs(1),
	}
	createCmd.Flags().StringVar(&in.AreaID, "area", "", "area id")
	createCmd.Flags().StringVar(&in.Description, "vision", "", "long-form description of the objective")
	createCmd.Flags().StringVar(&in.Horizon, "horizon", "", "e.g. 2026Q3")
	createCmd.RunE = runArgs(opts, func(rt *Runtime, args []string) int {
		const command = "objective.create"
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
		result, err := store.CreateObjective(ctx, rt.WriteContext(), in)
		if err != nil {
			return rt.EmitError(command, err)
		}
		return rt.Emit(command, result, func(w io.Writer, data any) error {
			if o, ok := data.(ops.Objective); ok {
				fmt.Fprintf(w, "created %s  %s\n", o.ID, o.Name)
			}
			return nil
		})
	})

	listCmd := &cobra.Command{Use: "list", Short: "List objectives with their key results"}
	var includeArchived bool
	listCmd.Flags().BoolVar(&includeArchived, "all", false, "include dropped and archived")
	listCmd.RunE = run(opts, func(rt *Runtime) int {
		const command = "objective.list"
		store, err := rt.OpsStore(true)
		if err != nil {
			return rt.EmitError(command, err)
		}
		ctx, cancel := rt.Context()
		defer cancel()
		trees, err := store.ListObjectives(ctx, includeArchived)
		if err != nil {
			return rt.EmitError(command, err)
		}
		return rt.EmitData(command, trees, func(w io.Writer, data any) error {
			items, ok := data.([]ops.ObjectiveTree)
			if !ok {
				return nil
			}
			if len(items) == 0 {
				fmt.Fprintln(w, "no objectives yet")
				return nil
			}
			for _, t := range items {
				fmt.Fprintf(w, "%s  %s", t.Objective.ID, t.Objective.Name)
				if t.Objective.Horizon != nil {
					fmt.Fprintf(w, "  [%s]", *t.Objective.Horizon)
				}
				fmt.Fprintln(w)
				for _, k := range t.KeyResults {
					fmt.Fprintf(w, "    %s  %s\n", k.ID, k.Name)
					fmt.Fprintf(w, "        %s\n", renderMetric(k.MetricName, k.MetricUnit, k.CurrentValue, k.TargetValue))
				}
			}
			return nil
		})
	})

	cmd.AddCommand(createCmd, listCmd)
	return cmd
}

// newKeyResultCmd is where a measurement is created and moved.
func newKeyResultCmd(opts *GlobalOptions) *cobra.Command {
	cmd := &cobra.Command{Use: "kr", Short: "Key results: one measurement each"}

	var in ops.CreateKeyResultInput
	var target, current, weight float64
	createCmd := &cobra.Command{
		Use:         "create <name>",
		Short:       "Create a key result under an objective",
		Annotations: map[string]string{"write": "true"},
		Args:        cobra.MaximumNArgs(1),
	}
	createCmd.Flags().StringVar(&in.ObjectiveID, "objective", "", "objective id (required)")
	createCmd.Flags().StringVar(&in.MetricName, "metric", "", "what is measured (required)")
	createCmd.Flags().StringVar(&in.MetricUnit, "unit", "", "unit of the metric")
	createCmd.Flags().Float64Var(&target, "target", 0, "target value")
	createCmd.Flags().Float64Var(&current, "current", 0, "current value")
	createCmd.Flags().Float64Var(&weight, "weight", 0, "share of the objective, 0..1")
	createCmd.Flags().StringVar(&in.Horizon, "horizon", "", "e.g. 2026Q3")
	createCmd.RunE = runArgs(opts, func(rt *Runtime, args []string) int {
		const command = "kr.create"
		if len(args) == 1 {
			in.Name = args[0]
		}
		bindFloat(createCmd, "target", &target, &in.TargetValue)
		bindFloat(createCmd, "current", &current, &in.CurrentValue)
		bindFloat(createCmd, "weight", &weight, &in.Weight)
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
		result, err := store.CreateKeyResult(ctx, rt.WriteContext(), in)
		if err != nil {
			return rt.EmitError(command, err)
		}
		return rt.Emit(command, result, func(w io.Writer, data any) error {
			if k, ok := data.(ops.KeyResult); ok {
				fmt.Fprintf(w, "created %s  %s\n    %s\n", k.ID, k.Name,
					renderMetric(k.MetricName, k.MetricUnit, k.CurrentValue, k.TargetValue))
			}
			return nil
		})
	})

	var up ops.UpdateKeyResultInput
	var upTarget, upCurrent, upWeight float64
	var upName, upStatus string
	updateCmd := &cobra.Command{
		Use:         "update <id>",
		Short:       "Patch a key result, typically to move its current value",
		Annotations: map[string]string{"write": "true"},
		Args:        cobra.ExactArgs(1),
	}
	updateCmd.Flags().Int64Var(&up.ExpectedVersion, "expected-version", 0, "version read before editing")
	updateCmd.Flags().StringVar(&upName, "name", "", "new name")
	updateCmd.Flags().StringVar(&upStatus, "status", "", "active|done|dropped|archived")
	updateCmd.Flags().Float64Var(&upTarget, "target", 0, "target value")
	updateCmd.Flags().Float64Var(&upCurrent, "current", 0, "current value")
	updateCmd.Flags().Float64Var(&upWeight, "weight", 0, "share of the objective, 0..1")
	updateCmd.RunE = runArgs(opts, func(rt *Runtime, args []string) int {
		const command = "kr.update"
		up.KeyResultID = args[0]
		setIfChanged(updateCmd, "name", upName, &up.Name)
		setIfChanged(updateCmd, "status", upStatus, &up.Status)
		bindFloat(updateCmd, "target", &upTarget, &up.TargetValue)
		bindFloat(updateCmd, "current", &upCurrent, &up.CurrentValue)
		bindFloat(updateCmd, "weight", &upWeight, &up.Weight)
		store, err := rt.OpsStore(false)
		if err != nil {
			return rt.EmitError(command, err)
		}
		ctx, cancel := rt.Context()
		defer cancel()
		result, err := store.UpdateKeyResult(ctx, rt.WriteContext(), up)
		if err != nil {
			return rt.EmitError(command, err)
		}
		return rt.Emit(command, result, func(w io.Writer, data any) error {
			if k, ok := data.(*ops.KeyResult); ok {
				fmt.Fprintf(w, "%s  %s\n    %s\n", k.ID, k.Name,
					renderMetric(k.MetricName, k.MetricUnit, k.CurrentValue, k.TargetValue))
			}
			return nil
		})
	})

	cmd.AddCommand(createCmd, updateCmd)
	return cmd
}

// newDepCmd exposes the dependency graph, which also had a table and no door.
func newDepCmd(opts *GlobalOptions) *cobra.Command {
	cmd := &cobra.Command{Use: "dep", Short: "Dependencies between any two nodes"}

	var in ops.AddDependencyInput
	addCmd := &cobra.Command{
		Use:         "add <from-id> <to-id>",
		Short:       "Record that one node blocks, requires, supports or relates to another",
		Annotations: map[string]string{"write": "true"},
		Args:        cobra.ExactArgs(2),
	}
	addCmd.Flags().StringVar(&in.FromType, "from-type", "task", ops.EntityTypeList())
	addCmd.Flags().StringVar(&in.ToType, "to-type", "task", ops.EntityTypeList())
	addCmd.Flags().StringVar(&in.DependencyType, "type", "blocks", "blocks|requires|related|supports")
	addCmd.Flags().IntVar(&in.LagDays, "lag", 0, "days that must pass after the upstream finishes")
	addCmd.Flags().StringVar(&in.Note, "note", "", "why this edge exists")
	addCmd.RunE = runArgs(opts, func(rt *Runtime, args []string) int {
		const command = "dep.add"
		in.FromID, in.ToID = args[0], args[1]
		store, err := rt.OpsStore(false)
		if err != nil {
			return rt.EmitError(command, err)
		}
		ctx, cancel := rt.Context()
		defer cancel()
		result, err := store.AddDependency(ctx, rt.WriteContext(), in)
		if err != nil {
			return rt.EmitError(command, err)
		}
		return rt.Emit(command, result, func(w io.Writer, data any) error {
			if d, ok := data.(ops.Dependency); ok {
				fmt.Fprintf(w, "%s %s --%s--> %s %s\n",
					d.FromType, d.FromID, d.DependencyType, d.ToType, d.ToID)
			}
			return nil
		})
	})

	var f ops.DependencyFilter
	listCmd := &cobra.Command{Use: "list [id]", Short: "List edges touching a node, both directions", Args: cobra.MaximumNArgs(1)}
	listCmd.Flags().StringVar(&f.EntityType, "entity-type", "task", "type of the node given")
	listCmd.Flags().StringVar(&f.Direction, "direction", "both", "both|outgoing|incoming")
	listCmd.Flags().StringVar(&f.Type, "type", "", "filter by dependency type")
	listCmd.RunE = runArgs(opts, func(rt *Runtime, args []string) int {
		const command = "dep.list"
		if len(args) == 1 {
			f.EntityID = args[0]
		}
		store, err := rt.OpsStore(true)
		if err != nil {
			return rt.EmitError(command, err)
		}
		ctx, cancel := rt.Context()
		defer cancel()
		deps, err := store.ListDependencies(ctx, f)
		if err != nil {
			return rt.EmitError(command, err)
		}
		return rt.EmitData(command, deps, func(w io.Writer, data any) error {
			items, ok := data.([]ops.Dependency)
			if !ok {
				return nil
			}
			if len(items) == 0 {
				fmt.Fprintln(w, "no dependencies")
				return nil
			}
			for _, d := range items {
				fmt.Fprintf(w, "%s %s --%s--> %s %s", d.FromType, d.FromID,
					d.DependencyType, d.ToType, d.ToID)
				if d.Note != nil {
					fmt.Fprintf(w, "  (%s)", *d.Note)
				}
				fmt.Fprintln(w)
			}
			return nil
		})
	})

	cmd.AddCommand(addCmd, listCmd)
	return cmd
}

// newTagCmd makes the tag vocabulary filterable instead of being a delimited
// string buried in a title.
func newTagCmd(opts *GlobalOptions) *cobra.Command {
	cmd := &cobra.Command{Use: "tag", Short: "Tags on any node"}

	var in ops.SetTagsInput
	var raw string
	setCmd := &cobra.Command{
		Use:         "set <id> <tags>",
		Short:       "Attach tags to a node (comma or pipe separated)",
		Annotations: map[string]string{"write": "true"},
		Args:        cobra.MinimumNArgs(1),
	}
	setCmd.Flags().StringVar(&in.EntityType, "entity-type", "task", ops.EntityTypeList())
	setCmd.Flags().BoolVar(&in.Replace, "replace", false, "make the given set authoritative")
	setCmd.RunE = runArgs(opts, func(rt *Runtime, args []string) int {
		const command = "tag.set"
		in.EntityID = args[0]
		if len(args) > 1 {
			raw = strings.Join(args[1:], ",")
		}
		in.Tags = []string{raw}
		store, err := rt.OpsStore(false)
		if err != nil {
			return rt.EmitError(command, err)
		}
		ctx, cancel := rt.Context()
		defer cancel()
		result, err := store.SetTags(ctx, rt.WriteContext(), in)
		if err != nil {
			return rt.EmitError(command, err)
		}
		return rt.Emit(command, result, func(w io.Writer, data any) error {
			if m, ok := data.(map[string]any); ok {
				fmt.Fprintf(w, "%v\n", m["tags"])
			}
			return nil
		})
	})

	var entityType, entityID string
	listCmd := &cobra.Command{Use: "list [id]", Short: "List the tag vocabulary with usage counts", Args: cobra.MaximumNArgs(1)}
	listCmd.Flags().StringVar(&entityType, "entity-type", "", "restrict to one entity type")
	listCmd.RunE = runArgs(opts, func(rt *Runtime, args []string) int {
		const command = "tag.list"
		if len(args) == 1 {
			entityID = args[0]
		}
		store, err := rt.OpsStore(true)
		if err != nil {
			return rt.EmitError(command, err)
		}
		ctx, cancel := rt.Context()
		defer cancel()
		tags, err := store.ListTags(ctx, entityType, entityID)
		if err != nil {
			return rt.EmitError(command, err)
		}
		return rt.EmitData(command, tags, func(w io.Writer, data any) error {
			items, ok := data.([]ops.TagCount)
			if !ok {
				return nil
			}
			if len(items) == 0 {
				fmt.Fprintln(w, "no tags")
				return nil
			}
			for _, t := range items {
				fmt.Fprintf(w, "%4d  %s\n", t.Count, t.Tag)
			}
			return nil
		})
	})

	cmd.AddCommand(setCmd, listCmd)
	return cmd
}

// newImportCmd carries a legacy OKR tree over in one process and one
// transaction. A hundred-odd nodes cannot be a hundred-odd CLI invocations:
// on iSH that is the difference between working and not.
func newImportCmd(opts *GlobalOptions) *cobra.Command {
	cmd := &cobra.Command{Use: "import", Short: "Bring an existing dataset into this instance"}

	var in ops.ImportLegacyInput
	okrCmd := &cobra.Command{
		Use:   "okr <path/to/tasks.db>",
		Short: "Import a legacy OKR tree (preview by default; --confirm writes)",
		Long: "Reads the source database read-only and reports exactly what it would\n" +
			"write, including every field mapping it had to choose. Nothing is\n" +
			"written until --confirm is given.",
		Annotations: map[string]string{"write": "true"},
		Args:        cobra.ExactArgs(1),
	}
	okrCmd.RunE = runArgs(opts, func(rt *Runtime, args []string) int {
		const command = "import.okr"
		in.SourcePath = args[0]
		in.Confirm = rt.Opts.Confirm
		store, err := rt.OpsStore(false)
		if err != nil {
			return rt.EmitError(command, err)
		}
		ctx, cancel := rt.Context()
		defer cancel()
		result, err := store.ImportLegacy(ctx, rt.WriteContext(), in)
		if err != nil {
			return rt.EmitError(command, err)
		}
		return rt.Emit(command, result, renderImportReport)
	})

	cmd.AddCommand(okrCmd)
	return cmd
}

func renderImportReport(w io.Writer, data any) error {
	r, ok := data.(*ops.ImportReport)
	if !ok {
		return nil
	}
	if r.Applied {
		fmt.Fprintf(w, "imported %s\n\n", r.Source)
	} else {
		fmt.Fprintf(w, "preview of %s — nothing written\n\n", r.Source)
	}

	fmt.Fprintln(w, "read from source:")
	for _, k := range sortedCountKeys(r.Read) {
		fmt.Fprintf(w, "  %-28s %d\n", k, r.Read[k])
	}
	if len(r.Written) > 0 {
		fmt.Fprintln(w, "\nwritten:")
		for _, k := range sortedCountKeys(r.Written) {
			fmt.Fprintf(w, "  %-28s %d\n", k, r.Written[k])
		}
	}
	if len(r.Mappings) > 0 {
		fmt.Fprintln(w, "\nmappings applied:")
		keys := make([]string, 0, len(r.Mappings))
		for k := range r.Mappings {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(w, "  %-16s %s\n", k, r.Mappings[k])
		}
	}
	if len(r.LinkSuggestions) > 0 {
		verb := "derived and written"
		if !r.Applied {
			verb = "would be written"
		}
		fmt.Fprintf(w, "\ntask -> milestone links %s (%d) — the legacy database has no such\ncolumn; these come from \"same project, same date\":\n", verb, len(r.LinkSuggestions))
		for _, l := range r.LinkSuggestions {
			fmt.Fprintf(w, "  %s\n", l)
		}
	}
	if len(r.Skipped) > 0 {
		fmt.Fprintf(w, "\nskipped (%d):\n", len(r.Skipped))
		for _, s := range r.Skipped {
			fmt.Fprintf(w, "  %s\n", s)
		}
	}
	for _, n := range r.NeedsInput {
		fmt.Fprintf(w, "\n%s\n", n)
	}
	return nil
}

func sortedCountKeys(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// renderMetric prints a measurement the way a review reads it.
func renderMetric(name string, unit *string, current, target *float64) string {
	u := ""
	if unit != nil {
		u = *unit
	}
	cur, tgt := "-", "-"
	if current != nil {
		cur = trimFloat(*current)
	}
	if target != nil {
		tgt = trimFloat(*target)
	}
	return fmt.Sprintf("%s %s/%s%s", name, cur, tgt, u)
}

func trimFloat(v float64) string {
	s := fmt.Sprintf("%.2f", v)
	s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	if s == "" || s == "-" {
		return "0"
	}
	return s
}

// bindFloat copies a flag into an optional field only when the user typed it,
// so an unset flag never writes a zero.
func bindFloat(cmd *cobra.Command, name string, value *float64, target **float64) {
	if cmd.Flags().Changed(name) {
		v := *value
		*target = &v
	}
}
