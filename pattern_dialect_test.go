package geta_test

import (
	"context"
	"strings"
	"testing"

	"github.com/koji-1009/geta/internal/vet"
)

// A pattern runs as RE2 and is published for ECMA-262, so what the two read
// otherwise is refused, by geta.New and getavet alike; what both read alike
// is kept.
func TestPatternsMeanTheSameInBothDialects(t *testing.T) {
	for pattern, want := range map[string]string{
		`^.+$`:        "uses '.', whose line terminators differ",
		`^\S+$`:       `uses \S, whose white space differs`,
		`a\sb`:        `uses \s, whose white space differs`,
		`[\s\d]`:      `uses \s, whose white space differs`,
		`(?i)^ab$`:    `opens a group with "(?i"`,
		`(?P<n>a)`:    `opens a group with "(?P"`,
		`\Aab`:        `uses \A`,
		`^a\z`:        `uses \z`,
		`\Qab\E`:      `uses \Q`,
		`\x{41}`:      `uses \x{…}`,
		`\pL`:         `uses \p without braces`,
		`[[:alpha:]]`: "uses a POSIX class",
		`[]a]`:        "puts ']' first in a class",
		`[^]a]`:       "puts ']' first in a class",
		`\\(?i)`:      `opens a group with "(?i"`,
		`[\]](?i)`:    `opens a group with "(?i"`,
		`a\012`:       "an octal escape here and a back reference there",
		`\p{Greek}`:   `uses \p{Greek}, which is not a general category`,
		`a\@b`:        `escapes '@', which needs no escape`,
		`\a`:          `uses \a`,
		`a\-b`:        `escapes '-' outside a class`,
		`a{`:          "uses '{' for itself",
		`a}`:          "uses '}' for itself",
		`[\[:a:]]`:    "uses ']' for itself",
		`^[\w-a]+$`:   `uses \w as a range's end`,
		`[\d-z]`:      `uses \d as a range's end`,
		`[\p{L}-x]`:   "uses a class escape as a range's end",
		`^*abc`:       "repeats an assertion",
		`a\b+`:        "repeats an assertion",
		`x$?`:         "repeats an assertion",
		`^{2}a`:       "repeats an assertion",
		`^[0-9]{02}$`: "uses '{' for itself",
		`a{1,02}`:     "uses '{' for itself",
		`(?<1a>x)`:    "names a group beginning with a digit",
	} {
		err := vet.CheckSchemaTag("pattern="+pattern, "string")
		if err == nil || !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), "ECMA-262") {
			t.Errorf("%s: %v; want %q", pattern, err, want)
		}
	}
	for _, pattern := range []string{`^[a-z]+$`, `(?:ab)+`, `(?<n>a)`, `\\A`, `[(?i)]`, `[\[:a:\]]`, `\p{L}`, `\p{Lu}+`, `\P{Any}`, `\p{Letter}`, `\p{Decimal_Number}`,
		`\x41`, `a\.b`, `[a\]]`, `^a{2}b{3}$`, `[ \t\d-]+`, `^[^\n]+$`, `a\/b`, `[a\-b]`, `[\w-]`, `[-\w]`, `[a-z\d]`, `^a+?$`, `(^a)$`, `[^-a]`, `^[a-z-\d]+$`, `[a-c-x]`, `a{0}`, `a{10,20}`, `(?<a1>x)`} {
		if err := vet.CheckSchemaTag("pattern="+pattern, "string"); err != nil {
			t.Errorf("%s: %v", pattern, err)
		}
	}
	type in struct {
		Q string `query:"q" schema:"pattern=^.+$"`
	}
	rejects(t, one("/x", get(func(context.Context, *in) (*ok, error) { return nil, nil })), "uses '.', whose line terminators differ")
}
