// Package cli turns the ops table into cobra commands and prints every result
// in one JSON envelope (or readable text on a terminal).
package cli

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/morethancoder/seox/internal/apperr"
	"github.com/morethancoder/seox/internal/ops"
	"github.com/morethancoder/seox/internal/web"
)

// Version is set at build time with -ldflags "-X .../cli.Version=...".
var Version = "dev"

var (
	gJSON, gHuman, gNoCache bool
	gFields                 string
	gTimeout                time.Duration
)

// Execute runs seox and returns the process exit code.
func Execute() int { return execute(os.Args[1:]) }

func execute(args []string) int {
	web.Version = Version
	// Ctrl-C and SIGTERM cancel the context, so crawls stop and child processes
	// (SearXNG runs in its own process group and never sees the terminal's Ctrl-C)
	// are cleaned up by the handler that started them.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	root := NewRoot()
	root.SetArgs(args)
	err := root.ExecuteContext(ctx)
	var ee *exitError
	switch {
	case err == nil:
		return 0
	case errors.As(err, &ee):
		return ee.code
	default:
		// cobra's own errors (unknown command, bad flag) still get the envelope.
		_ = emitErr(apperr.New(apperr.InvalidInput, "%s", err.Error()))
		return apperr.ExitCode(apperr.InvalidInput)
	}
}

// NewRoot builds the whole command tree from ops.All.
func NewRoot() *cobra.Command {
	root := &cobra.Command{
		Use:           "seox",
		Short:         "Free SEO for agents: audits, speed, ideas, rankings and authority for any site, real search data for yours",
		Version:       Version,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	pf := root.PersistentFlags()
	pf.BoolVar(&gJSON, "json", false, "JSON envelope output (default when stdout is not a terminal; also SEOX_JSON=1)")
	pf.BoolVar(&gHuman, "human", false, "readable output even when piped")
	pf.StringVar(&gFields, "fields", "", "keep only these comma-separated (dotted) fields of each result")
	pf.BoolVar(&gNoCache, "no-cache", false, "don't use cached results")
	pf.DurationVar(&gTimeout, "timeout", 0, "give up after this long (default per command: 90s, rank 3m, audit/compare/report 10m, none for login)")
	root.SetFlagErrorFunc(func(_ *cobra.Command, err error) error {
		return emitErr(apperr.New(apperr.InvalidInput, "%s", err.Error()))
	})

	for i := range ops.All {
		addOp(root, &ops.All[i])
	}
	root.AddCommand(schemaCmd())
	sortCommands(root)
	return root
}

// addOp creates the command for an op, and any group commands above it.
func addOp(root *cobra.Command, op *ops.Op) {
	words := strings.Fields(op.Command)
	parent := root
	for _, w := range words[:len(words)-1] {
		parent = child(parent, w)
	}
	name := words[len(words)-1]
	cmd := findChild(parent, name)
	if cmd == nil {
		cmd = &cobra.Command{Use: name}
		parent.AddCommand(cmd)
	}
	var posNames []string
	minArgs := 0
	variadic := false
	for _, p := range op.Params {
		if p.Positional {
			n := "<" + p.Name + ">"
			if p.Variadic {
				n += "..."
				variadic = true
			}
			posNames = append(posNames, n)
			if p.Required {
				minArgs++
			}
		}
	}
	cmd.Use = strings.Join(append([]string{name}, posNames...), " ")
	cmd.Short = op.Short
	maxArgs := len(posNames)
	cmd.Args = func(c *cobra.Command, args []string) error {
		if len(args) < minArgs || (!variadic && len(args) > maxArgs) {
			return emitErr(apperr.New(apperr.InvalidInput, "usage: seox %s", strings.TrimPrefix(c.CommandPath(), "seox ")+usageTail(posNames)))
		}
		return nil
	}
	for _, p := range op.Params {
		if p.Positional {
			continue
		}
		f := cmd.Flags()
		switch p.Type {
		case ops.TInt:
			d, _ := p.Default.(int)
			f.Int(p.Name, d, p.Help)
		case ops.TBool:
			f.Bool(p.Name, false, p.Help)
		case ops.TStrings:
			f.StringArray(p.Name, nil, p.Help)
		default:
			d, _ := p.Default.(string)
			f.String(p.Name, d, p.Help)
		}
	}
	cmd.RunE = func(c *cobra.Command, args []string) error { return run(c, op, args) }
}

func usageTail(pos []string) string {
	if len(pos) == 0 {
		return ""
	}
	return " " + strings.Join(pos, " ")
}

func findChild(parent *cobra.Command, name string) *cobra.Command {
	for _, c := range parent.Commands() {
		if c.Name() == name {
			return c
		}
	}
	return nil
}

// child returns (or creates) a group command.
func child(parent *cobra.Command, name string) *cobra.Command {
	if c := findChild(parent, name); c != nil {
		return c
	}
	c := &cobra.Command{Use: name, Short: name + " commands"}
	parent.AddCommand(c)
	return c
}

func sortCommands(c *cobra.Command) {
	cobra.EnableCommandSorting = true
	for _, s := range c.Commands() {
		sortCommands(s)
	}
}

// input turns args and flags into an ops.Input. A variadic positional takes
// every remaining argument as a []string.
func input(c *cobra.Command, op *ops.Op, args []string) (ops.Input, error) {
	in := ops.Input{Op: op, Values: map[string]any{}, Given: map[string]bool{}, NoCache: gNoCache}
	i := 0
	for _, p := range op.Params {
		if p.Positional {
			switch {
			case p.Variadic && i < len(args):
				in.Values[p.Name] = append([]string(nil), args[i:]...)
				in.Given[p.Name] = true
				i = len(args)
			case i < len(args):
				in.Values[p.Name] = args[i]
				in.Given[p.Name] = true
				i++
			}
			continue
		}
		f := c.Flags().Lookup(p.Name)
		if f == nil {
			continue
		}
		in.Given[p.Name] = f.Changed
		if !f.Changed {
			if p.Required {
				return in, apperr.New(apperr.InvalidInput, "--%s is required", p.Name)
			}
			if p.Default == nil {
				continue
			}
		}
		switch p.Type {
		case ops.TInt:
			v, _ := c.Flags().GetInt(p.Name)
			in.Values[p.Name] = v
		case ops.TBool:
			v, _ := c.Flags().GetBool(p.Name)
			in.Values[p.Name] = v
		case ops.TStrings:
			v, _ := c.Flags().GetStringArray(p.Name)
			in.Values[p.Name] = v
		default:
			v, _ := c.Flags().GetString(p.Name)
			in.Values[p.Name] = v
		}
	}
	return in, nil
}

// timeoutFor is --timeout when given, else the op's own limit from the table (0 = none).
func timeoutFor(op *ops.Op) time.Duration {
	if gTimeout > 0 {
		return gTimeout
	}
	return op.TimeoutFor()
}

// run executes one op and prints the result. A timeout or Ctrl-C ends it with the timeout code.
func run(c *cobra.Command, op *ops.Op, args []string) error {
	in, err := input(c, op, args)
	if err != nil {
		return emitErr(err)
	}
	ctx := c.Context()
	if ctx == nil {
		ctx = context.Background()
	}
	var cancel context.CancelFunc
	if d := timeoutFor(op); d > 0 {
		ctx, cancel = context.WithTimeout(ctx, d)
	} else {
		ctx, cancel = context.WithCancel(ctx)
	}
	defer cancel()
	res, err := ops.HandlerFor(op.Name)(ctx, in)
	if err != nil {
		return emitErr(err)
	}
	if res == nil {
		res = &ops.Result{}
	}
	return emitResult(res)
}

// Commands lists every leaf command path, sorted (for tests and docs).
func Commands() []string {
	var out []string
	var walk func(*cobra.Command)
	walk = func(c *cobra.Command) {
		if c.RunE != nil && c.HasParent() {
			out = append(out, strings.TrimPrefix(c.CommandPath(), "seox "))
		}
		for _, s := range c.Commands() {
			walk(s)
		}
	}
	walk(NewRoot())
	sort.Strings(out)
	return out
}
