package clients

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime/debug"
)

// goSDKVersion is the version of the Go SDK this binary was built with. A
// test binary does not always list its dependencies, so go.mod is the
// fallback.
func goSDKVersion() string {
	const path = "github.com/modelcontextprotocol/go-sdk"
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, d := range bi.Deps {
			if d.Path == path {
				return d.Version
			}
		}
	}
	for d := dir(); d != filepath.Dir(d); d = filepath.Dir(d) {
		b, err := os.ReadFile(filepath.Join(d, "go.mod"))
		if err != nil {
			continue
		}
		if m := regexp.MustCompile(regexp.QuoteMeta(path) + `\s+(v\S+)`).FindSubmatch(b); m != nil {
			return string(m[1])
		}
		break
	}
	return "unknown"
}
