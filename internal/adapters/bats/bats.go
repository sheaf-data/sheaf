// Package bats implements the bats test-parser adapter.
//
// Bats tests use `@test "name" { ... }` annotations in shell scripts.
// Recognition is a regex over .bats files.
package bats

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"regexp"

	"github.com/sheaf-data/sheaf/internal/adapters"
	commonpb "github.com/sheaf-data/sheaf/proto/common"
	testcasepb "github.com/sheaf-data/sheaf/proto/testcase"
)

const Name = "bats"
const Version = "0.1.0"

type Parser struct {
	include []string
	exclude []string
	rx      *regexp.Regexp
}

type Config struct {
	Include []string
	Exclude []string
}

func New(cfg Config) *Parser {
	include := cfg.Include
	if len(include) == 0 {
		include = []string{"**/*.bats"}
	}
	return &Parser{
		include: include,
		exclude: cfg.Exclude,
		// @test "name" {  or  @test 'name' {
		rx: regexp.MustCompile(`(?m)^\s*@test\s+["']([^"']+)["']\s*\{`),
	}
}

func (p *Parser) Name() string                  { return Name }
func (p *Parser) Version() string               { return Version }
func (p *Parser) SupportedFrameworks() []string { return []string{"bats"} }

func (p *Parser) Discover(ctx context.Context, repoRoot string, _ adapters.ScopeConfig) ([]*testcasepb.TestCase, error) {
	var out []*testcasepb.TestCase
	err := adapters.WalkMatching(repoRoot, p.include, p.exclude, func(rel string, _ fs.DirEntry) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		body, err := adapters.ReadFile(repoRoot, rel)
		if err != nil {
			return fmt.Errorf("bats: read %s: %w", rel, err)
		}
		offsets := computeLineOffsets(body)
		matches := p.rx.FindAllSubmatchIndex(body, -1)
		for _, m := range matches {
			name := string(body[m[2]:m[3]])
			line := lineFromOffset(offsets, m[0])
			h := sha256.Sum256(body[m[0]:m[1]])
			out = append(out, &testcasepb.TestCase{
				Id:         rel + "::" + name,
				Framework:  "bats",
				Location:   &commonpb.SourceLocation{Path: rel, Line: uint32(line)},
				Name:       name,
				SourceHash: hex.EncodeToString(h[:]),
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func computeLineOffsets(body []byte) []int {
	offsets := []int{0}
	for i, b := range body {
		if b == '\n' {
			offsets = append(offsets, i+1)
		}
	}
	return offsets
}

func lineFromOffset(offsets []int, off int) int {
	lo, hi := 0, len(offsets)-1
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if offsets[mid] <= off {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	return lo + 1
}
