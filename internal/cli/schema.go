package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"golang.org/x/term"

	"github.com/morethancoder/seox"
	"github.com/morethancoder/seox/internal/apperr"
	"github.com/morethancoder/seox/internal/ops"
)

type schemaOp struct {
	ops.Op
	Usage       string `json:"usage"`
	Implemented bool   `json:"implemented"`
}

type global struct {
	Name string `json:"name"`
	Help string `json:"help"`
}

// schemaCmd prints everything an agent needs to drive seox: every command with
// its params, kind (read/local), auth, cache and time limit, the global flags, the
// error codes by exit status and the embedded SKILL.md.
func schemaCmd() *cobra.Command {
	var noSkill bool
	c := &cobra.Command{
		Use:   "schema",
		Short: "Every command, its arguments, kind and auth, plus SKILL.md, as JSON (for agents)",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			list := make([]schemaOp, 0, len(ops.All))
			for _, o := range ops.All {
				list = append(list, schemaOp{Op: o, Usage: usage(o), Implemented: ops.Implemented(o.Name)})
			}
			var globals []global
			c.Root().PersistentFlags().VisitAll(func(f *pflag.Flag) {
				globals = append(globals, global{Name: "--" + f.Name, Help: f.Usage})
			})
			data := map[string]any{
				"name": "seox", "version": Version,
				"output": "JSON envelope {ok, data, error{code,message,detail}, next_cursor, warnings, meta{source, source_reason, quota_used, quota_left, cached}}; " +
					"JSON when stdout is not a terminal, with --json or SEOX_JSON=1",
				"writes":     "none: seox never changes a remote site; kind=local commands change only this machine (config, cache, Keychain, report files)",
				"ops":        list,
				"globals":    globals,
				"exit_codes": apperr.ExitCodes,
			}
			if !noSkill {
				data["skill"] = seox.Skill
			}
			return emitData(data)
		},
	}
	c.Flags().BoolVar(&noSkill, "no-skill", false, "leave SKILL.md out")
	return c
}

func usage(o ops.Op) string {
	parts := []string{"seox", o.Command}
	for _, p := range o.Params {
		switch {
		case p.Positional:
			parts = append(parts, "<"+p.Name+">")
		case p.Required:
			parts = append(parts, fmt.Sprintf("--%s <%s>", p.Name, p.Type))
		}
	}
	return strings.Join(parts, " ")
}

// readSecretTTY reads a hidden line from the terminal.
func readSecretTTY(prompt string) (string, error) {
	fmt.Fprint(os.Stderr, prompt)
	b, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	return string(b), err
}
