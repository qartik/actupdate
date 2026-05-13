package plan

import (
	"strings"
	"testing"
)

func TestRenderPlain(t *testing.T) {
	report := Report{
		Entries: []Entry{
			{FilePath: ".github/workflows/test.yml", Display: "actions/checkout@v4", Status: StatusUpdate, NewRef: "v6", Reason: "moving major tag"},
		},
		Counts: Counts{Updates: 1},
	}

	rendered := Render(report, RenderOptions{})
	if strings.Contains(rendered, "\x1b[") {
		t.Fatalf("expected plain output, got %q", rendered)
	}
	if !strings.Contains(rendered, "actions/checkout@v4 -> @v6") {
		t.Fatalf("expected update output, got %q", rendered)
	}
}

func TestRenderColor(t *testing.T) {
	report := Report{
		Entries: []Entry{
			{FilePath: ".github/workflows/test.yml", Display: "actions/checkout@v4", Status: StatusUpdate, NewRef: "v6", Reason: "moving major tag"},
		},
		Counts: Counts{Updates: 1},
	}

	rendered := Render(report, RenderOptions{Color: true})
	if !strings.Contains(rendered, "\x1b[") {
		t.Fatalf("expected ANSI color output, got %q", rendered)
	}
}
