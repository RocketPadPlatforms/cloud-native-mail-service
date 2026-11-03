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

package utils

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestGetProjectDir(t *testing.T) {
	// The project path itself contains /test/e2e, which the previous
	// string replacement would have stripped.
	root := filepath.Join(t.TempDir(), "test", "e2e", "proj")
	sub := filepath.Join(root, "test", "e2e")
	if err := os.MkdirAll(sub, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module x\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Chdir(sub)
	got, err := GetProjectDir()
	if err != nil {
		t.Fatal(err)
	}
	// Resolve symlinks (macOS /var -> /private/var) before comparing.
	want, _ := filepath.EvalSymlinks(root)
	got, _ = filepath.EvalSymlinks(got)
	if got != want {
		t.Fatalf("GetProjectDir() = %q, want %q", got, want)
	}
}

func TestRunDoesNotChangeTheWorkingDirectory(t *testing.T) {
	before, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Run(exec.Command("go", "version")); err != nil {
		t.Fatal(err)
	}
	after, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatalf("working directory changed from %q to %q", before, after)
	}
}
