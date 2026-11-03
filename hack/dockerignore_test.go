/*
Copyright 2026 Rocket Pad Platforms GmbH.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package hack

import (
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// ignored reports whether the .dockerignore rules exclude name (slash-separated,
// relative to the context root): the last matching rule wins, "!" re-includes.
// shortcut: no [..] classes or escapes, add them when .dockerignore needs them.
func ignored(rules []string, name string) bool {
	excluded := false
	for _, r := range rules {
		neg := strings.HasPrefix(r, "!")
		re := regexp.MustCompile("^" + globToRegexp(path.Clean(strings.TrimPrefix(r, "!"))) + "$")
		// A rule that matches a parent directory applies to everything below it.
		for p := name; p != "."; p = path.Dir(p) {
			if re.MatchString(p) {
				excluded = !neg
				break
			}
		}
	}
	return excluded
}

func globToRegexp(g string) string {
	var b strings.Builder
	for i := 0; i < len(g); i++ {
		switch {
		case strings.HasPrefix(g[i:], "**/"):
			b.WriteString("(.*/)?")
			i += 2
		case strings.HasPrefix(g[i:], "**"):
			b.WriteString(".*")
			i++
		case g[i] == '*':
			b.WriteString("[^/]*")
		case g[i] == '?':
			b.WriteString("[^/]")
		default:
			b.WriteString(regexp.QuoteMeta(g[i : i+1]))
		}
	}
	return b.String()
}

func TestIgnored(t *testing.T) {
	rules := []string{"**", "!**/*.go", "**/*_test.go", "!go.mod", "!a/b.lua"}
	for name, want := range map[string]bool{
		"cmd/main.go":      false,
		"main.go":          false,
		"cmd/main_test.go": true,
		"go.mod":           false,
		"go.sum":           true,
		"a/b.lua":          false,
		"a/c.lua":          true,
		"a/d/e.txt":        true,
	} {
		if got := ignored(rules, name); got != want {
			t.Errorf("ignored(%q) = %v, want %v", name, got, want)
		}
	}
}

// TestEmbeddedFilesReachImageBuild fails when .dockerignore would drop a file
// that a non-test //go:embed needs, which otherwise only breaks the image build.
func TestEmbeddedFilesReachImageBuild(t *testing.T) {
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, ".dockerignore"))
	if err != nil {
		t.Fatal(err)
	}
	var rules []string
	for l := range strings.SplitSeq(string(data), "\n") {
		if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "#") {
			rules = append(rules, l)
		}
	}

	embeds := 0
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if n := d.Name(); p != root && (strings.HasPrefix(n, ".") || n == "node_modules" || n == "bin") {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		src, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		for l := range strings.SplitSeq(string(src), "\n") {
			rest, ok := strings.CutPrefix(l, "//go:embed ")
			if !ok {
				continue
			}
			for pat := range strings.FieldsSeq(rest) {
				matches, err := filepath.Glob(filepath.Join(filepath.Dir(p), strings.TrimPrefix(pat, "all:")))
				if err != nil || len(matches) == 0 {
					t.Errorf("%s: embed pattern %q matches no files (%v)", p, pat, err)
				}
				for _, m := range matches {
					embeds++
					// shortcut: embedded directories are not expanded, add a walk when one is used.
					rel, _ := filepath.Rel(root, m)
					if ignored(rules, filepath.ToSlash(rel)) {
						t.Errorf("%s is embedded but excluded by .dockerignore", rel)
					}
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if embeds == 0 {
		t.Error("found no //go:embed files; the check is not looking in the right place")
	}
}
