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

package controller

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestEventMessageTruncates(t *testing.T) {
	short := "namespace \"a\" does not exist"
	if got := eventMessage(short); got != short {
		t.Errorf("eventMessage(short) = %q, want it unchanged", got)
	}
	exact := strings.Repeat("a", maxEventMessage)
	if got := eventMessage(exact); got != exact {
		t.Error("a message of exactly the limit must stay unchanged")
	}
	// Two-byte characters make the cut fall inside a character.
	for _, msg := range []string{
		strings.Repeat("a", maxEventMessage+1),
		strings.Repeat("ä", maxEventMessage),
		"x" + strings.Repeat("ä", maxEventMessage),
	} {
		got := eventMessage(msg)
		if len(got) > maxEventMessage || !strings.HasSuffix(got, "...") || !utf8.ValidString(got) {
			t.Errorf("eventMessage(%d bytes) = %d bytes, valid=%v", len(msg), len(got), utf8.ValidString(got))
		}
	}
}
