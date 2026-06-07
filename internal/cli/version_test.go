package cli

import "testing"

// Tests for `sheaf version`.

func TestVersion(t *testing.T) {
	if rc := Version(nil); rc != 0 {
		t.Errorf("Version rc = %d", rc)
	}
}
