// Smoke test for the workflows adapter. Exercises the per-fence
// extraction helper on a minimal markdown body; broader Parse() tests
// (file walking, multi-fence dedup, min_elements gating) can layer on
// top of this once the adapter sees production traffic. Initial
// coverage commitment: enough to detect a regression in the core
// "fence → invocation list" path.
package workflows

import (
	"reflect"
	"strings"
	"testing"
)

func TestExtractWorkflow_KubectlMultiStep(t *testing.T) {
	body := []byte(strings.Join([]string{
		"## Deploy and inspect",
		"",
		"Apply the manifest, then check the rollout:",
		"",
		"```sh",
		"$ kubectl apply -f deploy.yaml",
		"$ kubectl rollout status deployment/web",
		"$ kubectl get pods --selector=app=web",
		"```",
		"",
		"Done.",
	}, "\n"))

	got := extractWorkflow(body, "kubectl")

	want := []string{
		// kubectl apply (depth 2)
		"kubectl apply",
		// kubectl rollout status (depths 2 + 3, since maxSubcommandDepth=3
		// and "status" is a clean subcommand-token).
		"kubectl rollout",
		"kubectl rollout status",
		// kubectl get pods (depths 2 + 3)
		"kubectl get",
		"kubectl get pods",
		// --selector long-flag attaches to the deepest captured path on
		// the same line, which is "kubectl get pods".
		"kubectl get pods --selector",
	}

	if !reflect.DeepEqual(got, want) {
		t.Errorf("extractWorkflow refs mismatch\n got: %#v\nwant: %#v", got, want)
	}
}

func TestExtractWorkflow_NoFenceNoRefs(t *testing.T) {
	// Inline `kubectl apply` references outside a fenced block are
	// concept-doc mentions, not workflow steps. extractWorkflow must
	// return empty rather than mining the prose for invocations.
	body := []byte("Run `kubectl apply` to deploy. See also: `kubectl get pods`.")

	got := extractWorkflow(body, "kubectl")

	if len(got) != 0 {
		t.Errorf("inline-only body should produce no workflow refs; got %#v", got)
	}
}
