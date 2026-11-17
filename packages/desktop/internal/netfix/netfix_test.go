package netfix

import "testing"

func TestMatches(t *testing.T) {
	for h, want := range map[string]bool{
		"jooj-b1x.pages.dev": true, "a.b.workers.dev": true, "pages.dev.evil.com": false,
		"example.com": false, "1.2.3.4": false, "MOBO.PAGES.DEV.": true,
	} {
		if matches(h) != want {
			t.Errorf("%s: want %v", h, want)
		}
	}
}
