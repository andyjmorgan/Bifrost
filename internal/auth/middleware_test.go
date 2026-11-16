package auth

import "testing"

func TestBearerToken(t *testing.T) {
	cases := []struct {
		header string
		token  string
		ok     bool
	}{
		{"Bearer mcpg_abc", "mcpg_abc", true},
		{"bearer mcpg_abc", "mcpg_abc", true},
		{"BEARER mcpg_abc", "mcpg_abc", true},
		{"Bearer   mcpg_abc  ", "mcpg_abc", true},
		{"  bearer mcpg_abc", "mcpg_abc", true},
		{"Bearer ", "", false},
		{"Bearer", "", false},
		{"Basic dXNlcjpwYXNz", "", false},
		{"mcpg_abc", "", false},
		{"Bearermcpg_abc", "", false},
		{"", "", false},
	}
	for _, c := range cases {
		tok, ok := bearerToken(c.header)
		if tok != c.token || ok != c.ok {
			t.Errorf("bearerToken(%q) = %q, %v; want %q, %v", c.header, tok, ok, c.token, c.ok)
		}
	}
}
