package climatch

import (
	"reflect"
	"testing"
)

func TestInvocationRef(t *testing.T) {
	cases := []struct {
		line, binary, want string
	}{
		{"$ kubectl get pods", "kubectl", "kubectl get pods"},
		{"  kubectl apply -f deploy.yaml", "kubectl", "kubectl apply"},
		{"cat sine.wav | ffx audio play", "ffx", "ffx audio play"},
		{"$ ffx target list", "ffx", "ffx target list"},
		{"ffx component show /core --json", "ffx", "ffx component show"},
		{"    $ ffx audio gen sine --frequency 440", "ffx", "ffx audio gen sine"}, // 3 subcmd tokens kept
		{"docker run -it ubuntu", "ffx", ""},                                      // different binary
		{"ffx", "ffx", "ffx"},
		{"ffx --help", "ffx", "ffx"},
		{"ffx product-bundle get", "ffx", "ffx product-bundle get"},
	}
	for _, c := range cases {
		got := InvocationRef(c.line, c.binary)
		if got != c.want {
			t.Errorf("InvocationRef(%q, %q) = %q, want %q", c.line, c.binary, got, c.want)
		}
	}
}

func TestInvocationRef_DepthCap(t *testing.T) {
	// Four subcommand-shaped tokens: only the first 3 past the binary count.
	got := InvocationRef("ffx a b c d e", "ffx")
	if got != "ffx a b c" {
		t.Errorf("got %q, want %q (depth cap 3)", got, "ffx a b c")
	}
}

func TestFlagRefs(t *testing.T) {
	cases := []struct {
		name        string
		line        string
		binary      string
		commandPath string
		want        []string
	}{
		{
			name:        "flags after a full command",
			line:        "ffx audio gen sine --duration 5ms --frequency 440",
			binary:      "ffx",
			commandPath: "ffx audio gen sine",
			want:        []string{"ffx audio gen sine --duration", "ffx audio gen sine --frequency"},
		},
		{
			name:        "flag=value strips the value",
			line:        "ffx component show /core --machine=json",
			binary:      "ffx",
			commandPath: "ffx component show",
			want:        []string{"ffx component show --machine"},
		},
		{
			name:        "short flags are skipped",
			line:        "ffx target list -n 5 -o table",
			binary:      "ffx",
			commandPath: "ffx target list",
			want:        nil,
		},
		{
			name:        "bare -- is ignored",
			line:        "ffx target list -- --json",
			binary:      "ffx",
			commandPath: "ffx target list",
			// The bare "--" is skipped (len <= 2); the later --json is a real
			// long flag and is captured.
			want: []string{"ffx target list --json"},
		},
		{
			name:        "multiple flags preserve order and dedupe",
			line:        "ffx log --severity info --tag foo --severity warn",
			binary:      "ffx",
			commandPath: "ffx log",
			want:        []string{"ffx log --severity", "ffx log --tag"},
		},
		{
			name:        "no flags returns nil",
			line:        "ffx audio play sine.wav",
			binary:      "ffx",
			commandPath: "ffx audio play",
			want:        nil,
		},
		{
			name:        "a value token that is not a flag is not captured",
			line:        "ffx config set foo --json",
			binary:      "ffx",
			commandPath: "ffx config set",
			// "foo" is a bare value, not a flag; only --json is captured.
			want: []string{"ffx config set --json"},
		},
		{
			name:        "empty command path returns nil",
			line:        "ffx target list --json",
			binary:      "ffx",
			commandPath: "",
			want:        nil,
		},
		{
			name:        "binary not on line returns nil",
			line:        "docker run -it --rm ubuntu",
			binary:      "ffx",
			commandPath: "ffx run",
			want:        nil,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := FlagRefs(c.line, c.binary, c.commandPath)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("FlagRefs(%q, %q, %q) = %v, want %v", c.line, c.binary, c.commandPath, got, c.want)
			}
		})
	}
}

func TestPrefixes(t *testing.T) {
	cases := []struct {
		path     string
		minDepth int
		want     []string
	}{
		{"ffx audio gen", 2, []string{"ffx audio", "ffx audio gen"}},
		{"ffx target", 2, []string{"ffx target"}},
		{"ffx", 2, nil},
		{"ffx a b c", 1, []string{"ffx", "ffx a", "ffx a b", "ffx a b c"}},
	}
	for _, c := range cases {
		got := Prefixes(c.path, c.minDepth)
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("Prefixes(%q, %d) = %v, want %v", c.path, c.minDepth, got, c.want)
		}
	}
}
