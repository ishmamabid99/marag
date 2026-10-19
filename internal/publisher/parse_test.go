package publisher

import "testing"

func TestParseOwnerName(t *testing.T) {
	cases := []struct{ url, owner, name string }{
		{"https://github.com/acme/widgets", "acme", "widgets"},
		{"https://github.com/acme/widgets.git", "acme", "widgets"},
		{"https://gitlab.com/acme/widgets.git", "acme", "widgets"},
		{"git@github.com:acme/widgets.git", "acme", "widgets"},
	}
	for _, c := range cases {
		o, n, err := parseOwnerName(c.url)
		if err != nil || o != c.owner || n != c.name {
			t.Fatalf("%s → %q/%q err=%v", c.url, o, n, err)
		}
	}
	if _, _, err := parseOwnerName("not-a-url"); err == nil {
		t.Fatalf("expected error for bad url")
	}
}
