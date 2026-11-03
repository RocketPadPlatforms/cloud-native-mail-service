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

package dkim

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestEncodeDeterministic(t *testing.T) {
	t0 := time.Date(2026, 11, 3, 12, 0, 0, 123, time.FixedZone("x", 3600))
	a := State{
		Active:     []string{"b", "a"},
		Retiring:   []RetiringKey{{"z", t0}, {"y", t0.Add(time.Hour)}},
		VerifiedAt: map[string]time.Time{"q": t0, "p": t0},
	}
	b := State{
		Active:     []string{"a", "b"},
		Retiring:   []RetiringKey{{"y", t0.Add(time.Hour)}, {"z", t0}},
		VerifiedAt: map[string]time.Time{"p": t0, "q": t0},
	}
	const want = `{"v":1,"active":["a","b"],` +
		`"retiring":[{"selector":"y","until":"2026-11-03T12:00:00Z"},{"selector":"z","until":"2026-11-03T11:00:00Z"}],` +
		`"verifiedAt":{"p":"2026-11-03T11:00:00Z","q":"2026-11-03T11:00:00Z"}}`
	for _, s := range []State{a, b} {
		if got := Encode(s); got != want {
			t.Fatalf("got  %s\nwant %s", got, want)
		}
	}
	if got := Encode(State{}); got != `{"v":1}` {
		t.Fatalf("empty state: got %s", got)
	}
}

const wantCorrupt = "corrupt"

func TestDecode(t *testing.T) {
	t0 := time.Date(2026, 11, 3, 11, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name      string
		in        string
		want      State
		wantFound bool
		wantWarn  string
	}{
		{name: "empty is absent", in: ""},
		{name: "blank is absent", in: "  \n"},
		{
			name: "full", wantFound: true,
			in: `{"v":1,"active":["b","a"],"retiring":[{"selector":"z","until":"2026-11-03T11:00:00Z"}],"verifiedAt":{"p":"2026-11-03T11:00:00Z"}}`,
			want: State{
				Active:     []string{"a", "b"},
				Retiring:   []RetiringKey{{"z", t0}},
				VerifiedAt: map[string]time.Time{"p": t0},
			},
		},
		{name: "unknown fields are ignored", in: `{"v":1,"active":["a"],"future":{"x":1}}`, wantFound: true, want: State{Active: []string{"a"}}},
		{name: "minimal", in: `{"v":1}`, wantFound: true},
		{name: "corrupt json is absent with a warning", in: `{"v":1,"active":`, wantWarn: wantCorrupt},
		{name: "wrong type is absent with a warning", in: `{"v":1,"active":"a"}`, wantWarn: wantCorrupt},
		{name: "bad time is absent with a warning", in: `{"v":1,"retiring":[{"selector":"a","until":"soon"}]}`, wantWarn: wantCorrupt},
		{name: "missing version", in: `{"active":["a"]}`, wantWarn: "unsupported version 0"},
		{name: "newer version", in: `{"v":2,"active":["a"]}`, wantWarn: "unsupported version 2"},
		{name: "not an object", in: `[1]`, wantWarn: wantCorrupt},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, found, err := Decode(tc.in)
			if (err != nil) != (tc.wantWarn != "") || (err != nil && !strings.Contains(err.Error(), tc.wantWarn)) {
				t.Fatalf("got warning %v, want one containing %q", err, tc.wantWarn)
			}
			if found != tc.wantFound {
				t.Fatalf("found = %v, want %v", found, tc.wantFound)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestEncodeDecodeRoundTrip(t *testing.T) {
	t0 := time.Date(2026, 11, 3, 11, 0, 0, 0, time.UTC)
	in := State{
		Active:     []string{"a", "b"},
		Retiring:   []RetiringKey{{"z", t0}},
		VerifiedAt: map[string]time.Time{"p": t0},
	}
	got, found, err := Decode(Encode(in))
	if err != nil || !found || !reflect.DeepEqual(got, in) {
		t.Fatalf("got %+v found=%v err=%v, want %+v", got, found, err, in)
	}
	if Encode(got) != Encode(in) {
		t.Fatal("encoding is not stable across a round trip")
	}
}
