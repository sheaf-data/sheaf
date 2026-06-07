package pythontest

import _ "embed"

// extractHelperSource is the bundled Python AST helper, materialized to a
// temp file at runtime and run as a subprocess. Embedding keeps the adapter
// self-contained (no external script path to configure or ship separately).
//
//go:embed extract_ffx_invocations.py
var extractHelperSource string
