package ops

import (
	"strings"
	"testing"
	"time"
)

func TestTableIsConsistent(t *testing.T) {
	seen := map[string]bool{}
	for _, o := range All {
		if seen[o.Name] {
			t.Errorf("duplicate op %s", o.Name)
		}
		seen[o.Name] = true
		if o.Kind != Read && o.Kind != Local {
			t.Errorf("%s: kind %q", o.Name, o.Kind)
		}
		if o.Short == "" || o.Timeout == "" {
			t.Errorf("%s: summary and timeout are required", o.Name)
		}
		_ = o.TimeoutFor()
		for i, pr := range o.Params {
			if pr.Variadic && (!pr.Positional || i != len(o.Params)-1 && o.Params[i+1].Positional) {
				t.Errorf("%s: variadic %s must be the last positional", o.Name, pr.Name)
			}
			if pr.Positional && i > 0 && !o.Params[i-1].Positional {
				t.Errorf("%s: positionals must come first", o.Name)
			}
		}
	}
	for _, n := range strings.Fields("audit page speed keywords ideas rank authority compare login logout mine.queries mine.pages mine.drops mine.almost setup.verify report doctor config.show config.set cache.clear key.set key.remove") {
		if Find(n) == nil {
			t.Errorf("missing op %s", n)
		}
	}
}

func TestTimeouts(t *testing.T) {
	want := map[string]time.Duration{"audit": 10 * time.Minute, "compare": 10 * time.Minute, "report": 10 * time.Minute,
		"rank": 3 * time.Minute, "login": 0, "page": 90 * time.Second}
	for n, d := range want {
		if got := Find(n).TimeoutFor(); got != d {
			t.Errorf("%s timeout %v, want %v", n, got, d)
		}
	}
}

func TestStringsAndVariadicInput(t *testing.T) {
	in := Input{Values: map[string]any{"a": []string{"x", "y"}, "b": "z"}}
	if got := in.Strings("a"); len(got) != 2 || got[1] != "y" {
		t.Error(got)
	}
	if got := in.Strings("b"); len(got) != 1 || got[0] != "z" {
		t.Error(got)
	}
	if in.Strings("none") != nil {
		t.Error("absent should be nil")
	}
}
