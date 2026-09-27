// Package textdiff diffs files line by line for the diff page: per file,
// with both sides' line numbers, and with long unchanged runs folded.
package textdiff

import (
	"slices"
	"strings"

	"github.com/aymanbagabas/go-udiff/lcs"
)

// Op is what happened to a line.
type Op uint8

const (
	Equal Op = iota
	Delete
	Insert
)

func (o Op) String() string { return [...]string{"equal", "delete", "insert"}[o] }

// Line is one line of a diff. Old and New are 1-based line numbers on each
// side, 0 on the side the line isn't on.
type Line struct {
	Op       Op
	Old, New int
	Text     string
}

// Lines diffs two texts. A missing final newline doesn't count as a
// change.
func Lines(old, new string) []Line {
	a, b := split(old), split(new)
	var out []Line
	i, j := 0, 0
	equal := func(to int) {
		for ; i < to; i, j = i+1, j+1 {
			out = append(out, Line{Op: Equal, Old: i + 1, New: j + 1, Text: a[i]})
		}
	}
	for _, d := range lcs.DiffLines(a, b) {
		equal(d.Start)
		for ; i < d.End; i++ {
			out = append(out, Line{Op: Delete, Old: i + 1, Text: a[i]})
		}
		for ; j < d.ReplEnd; j++ {
			out = append(out, Line{Op: Insert, New: j + 1, Text: b[j]})
		}
	}
	equal(len(a))
	return out
}

func split(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}

// File statuses.
const (
	Added    = "added"
	Removed  = "removed"
	Modified = "modified"
)

// File is one file's diff.
type File struct {
	Path           string
	Status         string
	Lines          []Line
	Added, Removed int // lines
}

// Files diffs two sets of files by path, in path order. Files that are the
// same on both sides are left out.
func Files(old, new map[string]string) []File {
	var paths []string
	for p := range old {
		paths = append(paths, p)
	}
	for p := range new {
		if _, ok := old[p]; !ok {
			paths = append(paths, p)
		}
	}
	slices.Sort(paths)
	var files []File
	for _, p := range paths {
		o, inOld := old[p]
		n, inNew := new[p]
		if inOld && inNew && o == n {
			continue
		}
		f := File{Path: p, Status: Modified, Lines: Lines(o, n)}
		switch {
		case !inOld:
			f.Status = Added
		case !inNew:
			f.Status = Removed
		}
		for _, l := range f.Lines {
			switch l.Op {
			case Insert:
				f.Added++
			case Delete:
				f.Removed++
			}
		}
		files = append(files, f)
	}
	return files
}

// Block is a run of a file's lines: shown, or folded. Start and End index
// File.Lines, so a folded block can be fetched on its own.
type Block struct {
	Folded     bool
	Start, End int
	Lines      []Line
}

// Fold splits lines into blocks, folding unchanged runs except for context
// lines next to a change. A run is folded only if that hides more than
// two lines.
func Fold(lines []Line, context int) []Block {
	var blocks []Block
	shown := func(start, end int) {
		if start >= end {
			return
		}
		if n := len(blocks); n > 0 && !blocks[n-1].Folded {
			blocks[n-1].End = end
			blocks[n-1].Lines = lines[blocks[n-1].Start:end]
			return
		}
		blocks = append(blocks, Block{Start: start, End: end, Lines: lines[start:end]})
	}
	i := 0
	for i < len(lines) {
		if lines[i].Op != Equal {
			j := i
			for j < len(lines) && lines[j].Op != Equal {
				j++
			}
			shown(i, j)
			i = j
			continue
		}
		j := i
		for j < len(lines) && lines[j].Op == Equal {
			j++
		}
		// Keep context after the change before and before the next one.
		lo, hi := i, j
		if i > 0 {
			lo = min(i+context, j)
		}
		if j < len(lines) {
			hi = max(j-context, lo)
		}
		if hi-lo > 2 {
			shown(i, lo)
			blocks = append(blocks, Block{Folded: true, Start: lo, End: hi, Lines: lines[lo:hi]})
			shown(hi, j)
		} else {
			shown(i, j)
		}
		i = j
	}
	return blocks
}
