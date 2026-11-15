// Command hookctl runs Bifrost rules-engine scenarios and checks rule lists.
//
//	hookctl run [-summary] [-only ID] [files...]   run scenarios (stdin when no files)
//	hookctl check [file]                           compile a rule list (stdin when no file)
//
// Scenarios are JSON: one object, an array, or one object per line. run
// prints one JSON report per scenario (or a summary) and exits 1 when any
// scenario fails.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/andyjmorgan/Bifrost/pkg/hooks"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: hookctl run [-summary] [-only ID] [files...] | hookctl check [file]")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "run":
		os.Exit(run(os.Args[2:]))
	case "check":
		os.Exit(check(os.Args[2:]))
	default:
		fmt.Fprintln(os.Stderr, "unknown command "+os.Args[1])
		os.Exit(2)
	}
}

func run(args []string) int {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	summary := fs.Bool("summary", false, "print one line per scenario and totals")
	only := fs.String("only", "", "run only scenarios whose name or ids contain this")
	verbose := fs.Bool("v", false, "with -summary, print failing reports in full")
	_ = fs.Parse(args)
	var all []*hooks.Scenario
	files := fs.Args()
	if len(files) == 0 {
		b, err := io.ReadAll(os.Stdin)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
		ss, err := hooks.ParseScenarios(b)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
		for _, s := range ss {
			s.Dir, _ = os.Getwd()
		}
		all = ss
	}
	for _, f := range files {
		matches, _ := filepath.Glob(f)
		if len(matches) == 0 {
			matches = []string{f}
		}
		sort.Strings(matches)
		for _, m := range matches {
			ss, err := hooks.LoadScenarios(m)
			if err != nil {
				fmt.Fprintf(os.Stderr, "%s: %v\n", m, err)
				return 2
			}
			all = append(all, ss...)
		}
	}
	start := time.Now()
	counts := map[string]int{}
	failed := 0
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	for _, s := range all {
		if *only != "" && !strings.Contains(s.Name+" "+strings.Join(s.IDs, " "), *only) {
			continue
		}
		rep := s.Run(context.Background())
		counts[rep.Status]++
		if rep.Status == "fail" {
			failed++
		}
		if *summary {
			fmt.Printf("%-4s %-60s %s\n", strings.ToUpper(rep.Status), trim(rep.Name, 60), rep.Elapsed)
			for _, f := range rep.Failures {
				fmt.Printf("       %s\n", f)
			}
			if rep.Reason != "" && rep.Status != "pass" {
				fmt.Printf("       %s\n", rep.Reason)
			}
			if *verbose && rep.Status == "fail" {
				_ = enc.Encode(rep)
			}
			continue
		}
		_ = enc.Encode(rep)
	}
	if *summary {
		fmt.Printf("\n%d pass, %d fail, %d skip in %s\n", counts["pass"], counts["fail"], counts["skip"], time.Since(start).Round(time.Millisecond))
	}
	if failed > 0 {
		return 1
	}
	return 0
}

func check(args []string) int {
	var b []byte
	var err error
	if len(args) > 0 {
		b, err = os.ReadFile(args[0])
	} else {
		b, err = io.ReadAll(os.Stdin)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	rs, err := hooks.Parse(b)
	if err != nil {
		fmt.Println(err)
		return 1
	}
	set, probs := hooks.Compile(rs)
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if probs != nil {
		_ = enc.Encode(map[string]any{"ok": false, "problems": probs})
		return 1
	}
	_ = enc.Encode(map[string]any{"ok": true, "dynamic": set.Dynamic()})
	return 0
}

func trim(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
