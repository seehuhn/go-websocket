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
