package ffxinvoke

import (
	"reflect"
	"testing"
)

func TestCanonicalize_LiteralCommand(t *testing.T) {
	cmd, flags, stats := Canonicalize([]string{"target", "list"})
	wantCmd := []string{"ffx target", "ffx target list"}
	if !reflect.DeepEqual(cmd, wantCmd) {
		t.Errorf("cmd refs = %v, want %v", cmd, wantCmd)
	}
	if len(flags) != 0 {
		t.Errorf("flag refs = %v, want none", flags)
	}
	if stats.FullyDynamic != 0 {
		t.Errorf("FullyDynamic = %d, want 0", stats.FullyDynamic)
	}
}

func TestCanonicalize_CommandWithFlags(t *testing.T) {
	cmd, flags, _ := Canonicalize([]string{"target", "list", "--no-usb", "--no-probe"})
	wantCmd := []string{"ffx target", "ffx target list"}
	if !reflect.DeepEqual(cmd, wantCmd) {
		t.Errorf("cmd refs = %v, want %v", cmd, wantCmd)
	}
	wantFlags := []string{"ffx target list --no-usb", "ffx target list --no-probe"}
	if !reflect.DeepEqual(flags, wantFlags) {
		t.Errorf("flag refs = %v, want %v", flags, wantFlags)
	}
}

func TestCanonicalize_FlagWithValueToken(t *testing.T) {
	// "--timeout 0" — the flag is credited, the value "0" is a positional.
	cmd, flags, _ := Canonicalize([]string{"target", "wait", "--timeout", "0"})
	if got := []string{"ffx target", "ffx target wait"}; !reflect.DeepEqual(cmd, got) {
		t.Errorf("cmd refs = %v, want %v", cmd, got)
	}
	want := []string{"ffx target wait --timeout"}
	if !reflect.DeepEqual(flags, want) {
		t.Errorf("flag refs = %v, want %v", flags, want)
	}
}

func TestCanonicalize_LeadingGlobalStripped(t *testing.T) {
	// A leading global string flag (--machine json) must be stripped so the
	// subcommand path resolves past it, and credited at the ffx root.
	cmd, flags, _ := Canonicalize([]string{"--machine", "json", "target", "list", "--format", "a"})
	wantCmd := []string{"ffx target", "ffx target list"}
	if !reflect.DeepEqual(cmd, wantCmd) {
		t.Errorf("cmd refs = %v, want %v", cmd, wantCmd)
	}
	// The per-command flag and the global flag (at root) are both credited.
	wantFlags := []string{"ffx target list --format", "ffx --machine"}
	if !reflect.DeepEqual(flags, wantFlags) {
		t.Errorf("flag refs = %v, want %v", flags, wantFlags)
	}
}

func TestCanonicalize_FullyDynamic(t *testing.T) {
	// No literal subcommand token at all (an empty args list models a call
	// whose args were entirely dynamic and elided by the caller's slicer).
	cmd, flags, stats := Canonicalize(nil)
	if cmd != nil || flags != nil {
		t.Errorf("expected no refs, got cmd=%v flags=%v", cmd, flags)
	}
	if stats.FullyDynamic != 1 {
		t.Errorf("FullyDynamic = %d, want 1", stats.FullyDynamic)
	}
}

func TestCanonicalize_FlagOnlyNoSubcommand_NotBareFfx(t *testing.T) {
	// Flag-only args with no subcommand must NOT emit a bare "ffx" command
	// ref; it counts as fully dynamic when there is no global to credit.
	cmd, flags, stats := Canonicalize([]string{"--unknown-local"})
	if len(cmd) != 0 {
		t.Errorf("cmd refs = %v, want none (no bare ffx)", cmd)
	}
	if len(flags) != 0 {
		t.Errorf("flag refs = %v, want none", flags)
	}
	if stats.FullyDynamic != 1 {
		t.Errorf("FullyDynamic = %d, want 1", stats.FullyDynamic)
	}
}

func TestCanonicalize_BlankLiteralsDropped(t *testing.T) {
	cmd, _, stats := Canonicalize([]string{"", "target", "  ", "show"})
	want := []string{"ffx target", "ffx target show"}
	if !reflect.DeepEqual(cmd, want) {
		t.Errorf("cmd refs = %v, want %v", cmd, want)
	}
	if stats.FullyDynamic != 0 {
		t.Errorf("FullyDynamic = %d, want 0", stats.FullyDynamic)
	}
}
