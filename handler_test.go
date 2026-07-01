// seehuhn.de/go/websocket - an http server to establish websocket connections
// Copyright (C) 2026  Jochen Voss <voss@seehuhn.de>
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU General Public License for more details.
//
// You should have received a copy of the GNU General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.

package websocket

import "testing"

func TestContainsToken(t *testing.T) {
	type testCase struct {
		s, token string
		result   bool
	}
	testCases := []testCase{
		{"", "test", false},
		{"test", "test", true},
		{"test", "test1", false},
		{"testing", "test", false},
		{"test1", "test", false},
		{"test1, test, test2", "test", true},
		{"example/1, foo/2", "example", true},
		{"example/1, foo/2", "foo", true},
	}
	for _, tc := range testCases {
		if containsTokenFold([]string{tc.s}, tc.token) != tc.result {
			t.Errorf("containsToken(%q, %q) != %v", tc.s, tc.token, tc.result)
		}
	}
}
