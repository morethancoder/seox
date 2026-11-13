package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"golang.org/x/term"

	"github.com/morethancoder/seox/internal/apperr"
	"github.com/morethancoder/seox/internal/ops"
)

// Envelope is the one output shape of every command.
type Envelope struct {
	OK         bool          `json:"ok"`
	Data       any           `json:"data,omitempty"`
	Error      *apperr.Error `json:"error,omitempty"`
	NextCursor string        `json:"next_cursor,omitempty"`
	Warnings   []string      `json:"warnings,omitempty"`
	Meta       *ops.Meta     `json:"meta,omitempty"`
}

// exitError carries the exit code out of a command.
type exitError struct{ code int }

func (e *exitError) Error() string { return fmt.Sprintf("exit %d", e.code) }

func isTTY(f *os.File) bool { return term.IsTerminal(int(f.Fd())) }

// stdout is where envelopes go (swapped in tests).
var stdout io.Writer = os.Stdout

// jsonMode is on with --json, SEOX_JSON=1, or when stdout is not a terminal
// (so agents and pipes always get JSON). --human forces text.
func jsonMode() bool {
	if gJSON || os.Getenv("SEOX_JSON") == "1" {
		return true
	}
	if gHuman {
		return false
	}
	f, ok := stdout.(*os.File)
	return !ok || !isTTY(f)
}

// emit prints an envelope and returns the matching exit error.
func emit(env *Envelope) error {
	if env.OK && gFields != "" && env.Data != nil {
		env.Data = filterFields(env.Data, strings.Split(gFields, ","))
	}
	if env.Meta != nil && *env.Meta == (ops.Meta{}) {
		env.Meta = nil
	}
	if jsonMode() {
		enc := json.NewEncoder(stdout)
		enc.SetEscapeHTML(false)
		_ = enc.Encode(env)
	} else {
		printHuman(stdout, env)
	}
	if !env.OK {
		return &exitError{apperr.ExitCode(env.Error.Code)}
	}
	return nil
}

func emitErr(err error) error { return emit(&Envelope{OK: false, Error: apperr.From(err)}) }

func emitData(v any) error { return emit(&Envelope{OK: true, Data: v}) }

func emitResult(r *ops.Result) error {
	if r == nil {
		r = &ops.Result{}
	}
	m := r.Meta
	return emit(&Envelope{OK: true, Data: r.Data, NextCursor: r.NextCursor, Warnings: r.Warnings, Meta: &m})
}

func printHuman(w io.Writer, env *Envelope) {
	if !env.OK {
		fmt.Fprintf(w, "error (%s): %s\n", env.Error.Code, env.Error.Message)
		if env.Error.Detail != nil {
			b, _ := json.MarshalIndent(env.Error.Detail, "", "  ")
			fmt.Fprintln(w, string(b))
		}
		return
	}
	for _, warn := range env.Warnings {
		fmt.Fprintf(w, "note: %s\n", warn)
	}
	if env.Data == nil {
		fmt.Fprintln(w, "ok")
	} else if s, ok := env.Data.(string); ok {
		fmt.Fprintln(w, s)
	} else {
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		enc.SetIndent("", "  ")
		_ = enc.Encode(env.Data)
		fmt.Fprint(w, buf.String())
	}
	if env.NextCursor != "" {
		fmt.Fprintf(w, "next_cursor: %s\n", env.NextCursor)
	}
	if m := env.Meta; m != nil && m.Source != "" {
		line := "via " + m.Source
		if m.SourceReason != "" {
			line += " (" + m.SourceReason + ")"
		}
		if m.QuotaUsed > 0 {
			line += fmt.Sprintf(", %d calls used, %d left today", m.QuotaUsed, m.QuotaLeft)
		}
		if m.Cached {
			line += ", cached"
		}
		fmt.Fprintln(os.Stderr, line)
	}
}

// filterFields keeps only the listed (dotted) fields of each object.
func filterFields(data any, fields []string) any {
	raw, err := json.Marshal(data)
	if err != nil {
		return data
	}
	var v any
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if d.Decode(&v) != nil {
		return data
	}
	pick := func(m map[string]any) map[string]any {
		o := map[string]any{}
		for _, f := range fields {
			if f = strings.TrimSpace(f); f == "" {
				continue
			}
			if val, ok := dig(m, strings.Split(f, ".")); ok {
				o[f] = val
			}
		}
		return o
	}
	switch x := v.(type) {
	case []any:
		res := make([]any, 0, len(x))
		for _, it := range x {
			if m, ok := it.(map[string]any); ok {
				res = append(res, pick(m))
			} else {
				res = append(res, it)
			}
		}
		return res
	case map[string]any:
		return pick(x)
	}
	return v
}

func dig(m map[string]any, path []string) (any, bool) {
	var cur any = m
	for _, p := range path {
		mm, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		if cur, ok = mm[p]; !ok {
			return nil, false
		}
	}
	return cur, true
}
