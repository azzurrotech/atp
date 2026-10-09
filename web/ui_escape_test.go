package web

import (
	"strings"
	"testing"
)

// TestBaseEscaperEscapesHTML guards the browser-side esc() helper in
// templates/base.html. Several pages build table rows with innerHTML and
// interpolate API values through esc(), so esc() must map every dangerous
// character to an HTML entity. A regression to an identity mapping — the
// historical bug where the map was {"&":"&", "<":"<", ...} — makes those pages
// render stored values as live markup, so this test fails loudly if it returns.
func TestBaseEscaperEscapesHTML(t *testing.T) {
	b, err := uiFS.ReadFile("templates/base.html")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	mappings := []struct {
		js    string
		plain string
	}{
		{`"&":"&amp;"`, "&"},
		{`"<":"&lt;"`, "<"},
		{`">":"&gt;"`, ">"},
		{`"\"":"&quot;"`, `"`},
		{`"'":"&#39;"`, "'"},
	}
	for _, m := range mappings {
		identity := `"` + m.plain + `":"` + m.plain + `"`
		if strings.Contains(src, identity) && !strings.Contains(src, m.js) {
			t.Errorf("esc() maps %q to itself (identity) in templates/base.html; it must map to %s", m.plain, m.js)
		}
		if !strings.Contains(src, m.js) {
			t.Errorf("esc() mapping missing %s in templates/base.html", m.js)
		}
	}
}
