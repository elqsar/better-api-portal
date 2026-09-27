package textdiff

import (
	"fmt"
	"strings"
	"testing"
)

// render shows lines as " n m text", "-n   text", "+  m text".
func render(lines []Line) string {
	var b strings.Builder
	for _, l := range lines {
		fmt.Fprintf(&b, "%c%d %d %s\n", " -+"[l.Op], l.Old, l.New, l.Text)
	}
	return b.String()
}

func TestLines(t *testing.T) {
	got := render(Lines("a\nb\nc\n", "a\nB\nc\nd"))
	want := " 1 1 a\n-2 0 b\n+0 2 B\n 3 3 c\n+0 4 d\n"
	if got != want {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
	if got := render(Lines("", "x\n")); got != "+0 1 x\n" {
		t.Errorf("from empty: %q", got)
	}
	if got := Lines("same\n", "same"); len(got) != 1 || got[0].Op != Equal {
		t.Errorf("final newline counted: %+v", got)
	}
}

func TestFiles(t *testing.T) {
	files := Files(
		map[string]string{"a.yaml": "x\n", "b.yaml": "same\n", "c.yaml": "gone\n"},
		map[string]string{"a.yaml": "y\n", "b.yaml": "same\n", "d.yaml": "new\nnew\n"})
	var got []string
	for _, f := range files {
		got = append(got, fmt.Sprintf("%s %s +%d -%d", f.Path, f.Status, f.Added, f.Removed))
	}
	want := []string{"a.yaml modified +1 -1", "c.yaml removed +0 -1", "d.yaml added +2 -0"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestFold(t *testing.T) {
	var old, new []string
	for i := range 30 {
		old = append(old, fmt.Sprint(i))
		new = append(new, fmt.Sprint(i))
	}
	new[10], new[25] = "ten", "twenty-five"
	lines := Lines(strings.Join(old, "\n"), strings.Join(new, "\n"))
	var got []string
	for _, b := range Fold(lines, 3) {
		got = append(got, fmt.Sprintf("%v %d-%d", b.Folded, b.Start, b.End))
		if len(b.Lines) != b.End-b.Start {
			t.Errorf("block %d-%d has %d lines", b.Start, b.End, len(b.Lines))
		}
	}
	// 32 lines: 0–9 equal, 10–11 the change, 12–25 equal, 26–27 the change,
	// 28–31 equal. The gap between the changes keeps 3 lines each side; the
	// last run would hide only one line after its context, so it shows.
	want := []string{"true 0-7", "false 7-15", "true 15-23", "false 23-32"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("blocks %q, want %q", got, want)
	}
	// Folding two lines or fewer isn't worth it.
	short := Lines("a\nb\nc\nd\ne", "a\nb\nc\nd\nE")
	if blocks := Fold(short, 3); len(blocks) != 1 || blocks[0].Folded {
		t.Errorf("short: %+v", blocks)
	}
}
