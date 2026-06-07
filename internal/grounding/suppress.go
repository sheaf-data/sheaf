package grounding

import (
	"bufio"
	"os"
	"strings"
)

// Suppression honors a checked-in, snapshot-honest ".sheafignore"-style
// list so a reviewed "this collision is fine" does not resurrect every
// run (REQUIREMENTS §5 "Suppression"; load-bearing for retention and
// support-load). It is deliberately line-oriented and reviewable in a PR.
//
// File format (one rule per line; blank lines and `#` comments ignored):
//
//	# grounding suppressions for fuchsia.driver.framework
//	grounding <source_path> <token>            # suppress this token on this doc
//	grounding <source_path> <token> <element>  # …only for this element_id
//	grounding <source_path> *                  # suppress every finding on this doc
//
// The leading "grounding" keyword namespaces the file so it can later host
// other surfaces' suppressions without ambiguity. Matching is exact on
// source_path and case-insensitive on token; element is matched exactly
// when present. "*" as the token suppresses the whole file.
type Suppression struct {
	// byDocToken[source_path][lower(token)] -> set of element_ids ("" = any).
	byDocToken map[string]map[string]map[string]bool
	// wholeDoc[source_path] = true suppresses every finding on that doc.
	wholeDoc map[string]bool
}

// emptySuppression is the no-op suppressor used when no file is configured.
func emptySuppression() *Suppression {
	return &Suppression{
		byDocToken: map[string]map[string]map[string]bool{},
		wholeDoc:   map[string]bool{},
	}
}

// LoadSuppression reads a .sheafignore-style file. A missing path yields an
// empty (no-op) suppressor with no error — suppression is optional. Parse
// errors on individual lines are skipped silently (the line is ignored);
// a genuinely unreadable file returns the error.
func LoadSuppression(path string) (*Suppression, error) {
	s := emptySuppression()
	if path == "" {
		return s, nil
	}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return s, nil
		}
		return nil, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// Strip a trailing inline comment.
		if i := strings.Index(line, " #"); i >= 0 {
			line = strings.TrimSpace(line[:i])
		}
		fields := strings.Fields(line)
		if len(fields) < 3 || fields[0] != "grounding" {
			continue
		}
		doc := fields[1]
		token := fields[2]
		if token == "*" {
			s.wholeDoc[doc] = true
			continue
		}
		elem := ""
		if len(fields) >= 4 {
			elem = fields[3]
		}
		s.add(doc, token, elem)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Suppression) add(doc, token, elem string) {
	lt := strings.ToLower(token)
	if s.byDocToken[doc] == nil {
		s.byDocToken[doc] = map[string]map[string]bool{}
	}
	if s.byDocToken[doc][lt] == nil {
		s.byDocToken[doc][lt] = map[string]bool{}
	}
	s.byDocToken[doc][lt][elem] = true
}

// suppressed reports whether a finding for (sourcePath, token, elementID)
// is suppressed. An element-scoped rule matches that element; an
// element-blank ("") rule matches any element with that token on that doc.
func (s *Suppression) suppressed(sourcePath, token, elementID string) bool {
	if s == nil {
		return false
	}
	if s.wholeDoc[sourcePath] {
		return true
	}
	bt := s.byDocToken[sourcePath]
	if bt == nil {
		return false
	}
	elems := bt[strings.ToLower(token)]
	if elems == nil {
		return false
	}
	return elems[""] || elems[elementID]
}
