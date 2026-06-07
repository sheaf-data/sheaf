// Tiny shim file: aliases the categorizationpb.Rules type so extra.go
// can refer to *categorizationpb_Rules without re-importing the
// package twice. Keep this file changes-only.
package cli

import categorizationpb "github.com/sheaf-data/sheaf/proto/categorization"

type ourCategorizationRules = categorizationpb.Rules
