package slug

import "testing"

func TestBase(t *testing.T) {
	cases := map[string]string{
		"Atlassian":                 "atlassian",
		"My Super Long Server Name": "mysuperlongs",
		"  ---  ":                   "server",
		"Jira (prod) #2":            "jiraprod2",
	}
	for in, want := range cases {
		if got := Base(in); got != want {
			t.Errorf("Base(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCandidatesCollisions(t *testing.T) {
	taken := map[string]bool{}
	var got []string
	for range 12 {
		Candidates("atlassian", func(c string) bool {
			if taken[c] {
				return false
			}
			taken[c] = true
			got = append(got, c)
			return true
		})
	}
	want := []string{"atlassian", "atlassian1", "atlassian2", "atlassian3", "atlassian4", "atlassian5",
		"atlassian6", "atlassian7", "atlassian8", "atlassian9", "atlassian10", "atlassian11"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("candidate %d = %q, want %q (all %v)", i, got[i], want[i], got)
		}
	}
	// A full-length base must overwrite its tail.
	taken = map[string]bool{"mysuperlongs": true, "mysuperlong1": true}
	Candidates("mysuperlongs", func(c string) bool {
		if taken[c] {
			return false
		}
		if c != "mysuperlong2" {
			t.Fatalf("got %q", c)
		}
		return true
	})
}
