// Command geta writes and checks the route table of a geta tree.
//
//	geta sync [-check] [dir]   write dir/zz_routes.go and its assembly test
//	                           dir/zz_routes_test.go (default dir: routes)
//	geta check [dir]           name each directory where table and tree disagree
//
// sync reads only directory names and whether route.go and scope.go exist;
// the compiler checks the generated table. If the root directory has
// options.go defining func Options(env Env) []geta.Option, the assembly test
// passes Options(env) to geta.New, as main does.
//
// sync writes each file whole or not at all. Neither command follows a
// symbolic link, which ./... does not follow either; one to a directory
// holding route.go or scope.go, the root included, is an error.
//
// The exit status is 0 when there is nothing to report, 1 after reporting
// findings or on an unreadable file or tree, and 2 on a usage error, an
// empty directory argument included. Flags must precede arguments.
package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/koji-1009/geta/internal/tree"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stderr))
}

const (
	syncUsage  = "geta sync [-check] [dir]"
	checkUsage = "geta check [dir]"
	usage      = syncUsage + " | " + checkUsage
)

// flags returns the subcommand's flag set, which prints use on a bad flag.
func flags(name, use string, stderr io.Writer) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprintln(stderr, "usage:", use) }
	return fs
}

// parse parses args, allowing at most most arguments. It reports false after
// printing a usage error.
func parse(fs *flag.FlagSet, use string, args []string, most int, stderr io.Writer) bool {
	if fs.Parse(args) != nil {
		return false // already reported by the flag package
	}
	if n := fs.NArg(); n > most {
		fmt.Fprintf(stderr, "geta %s: %d arguments %q; usage: %s\n", fs.Name(), n, fs.Args(), use)
		return false
	}
	// An empty directory would be the working directory, which no one names
	// so; it is most likely an unset variable.
	if fs.NArg() == 1 && fs.Arg(0) == "" {
		fmt.Fprintf(stderr, "geta %s: an empty directory argument; usage: %s\n", fs.Name(), use)
		return false
	}
	return true
}

func run(args []string, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage:", usage)
		return 2
	}
	switch args[0] {
	case "sync":
		fs := flags("sync", syncUsage, stderr)
		check := fs.Bool("check", false, "write nothing; fail if a sync would change the table")
		if !parse(fs, syncUsage, args[1:], 1, stderr) {
			return 2
		}
		t, err := tree.Scan(dirArg(fs.Args()))
		if err != nil {
			fmt.Fprintln(stderr, "geta sync:", err)
			return 1
		}
		changed, err := t.Sync(*check)
		if err != nil {
			fmt.Fprintln(stderr, "geta sync:", err)
			return 1
		}
		if *check && len(changed) > 0 {
			for _, name := range changed {
				fmt.Fprintf(stderr, "geta sync -check: %s is out of date; run geta sync\n",
					filepath.Join(t.Dir, name))
			}
			return 1
		}
		return 0
	case "check":
		fs := flags("check", checkUsage, stderr)
		if !parse(fs, checkUsage, args[1:], 1, stderr) {
			return 2
		}
		t, err := tree.Scan(dirArg(fs.Args()))
		if err != nil {
			fmt.Fprintln(stderr, "geta check:", err)
			return 1
		}
		problems, err := t.Check()
		if err != nil {
			fmt.Fprintln(stderr, "geta check:", err)
			return 1
		}
		for _, p := range problems {
			fmt.Fprintln(stderr, p)
		}
		if len(problems) > 0 {
			return 1
		}
		return 0
	}
	fmt.Fprintf(stderr, "geta: unknown command %q; usage: %s\n", args[0], usage)
	return 2
}

func dirArg(args []string) string {
	if len(args) > 0 {
		return args[0]
	}
	return "routes"
}
