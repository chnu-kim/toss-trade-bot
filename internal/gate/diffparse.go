package gate

import "strings"

// ParseUnifiedDiff parses `git diff`/`gh pr diff`-style unified diff text
// into a []DiffFile SanityCheck can cross-reference a verdict's evidence
// citations against. This is read as inert data (ADR-0011 point 4(b)) — it
// never shells out, never touches disk, and never executes anything the
// diff contains; it only splits text into (file path, hunk text) pairs.
//
// A file's Path is taken from its "+++ b/<path>" line (the post-change
// path), which also correctly names a newly-added file — except for a
// deleted file, whose "+++" line is literally "+++ /dev/null"; for that case
// the "--- a/<path>" line (the pre-change path) is used instead, so a
// deleted file's Path is its real, pre-deletion path, not the literal string
// "/dev/null" (codex:review finding — otherwise an evidence citation against
// the real deleted-file path could never be grounded by SanityCheck). Every
// other file uses its "+++" path, matching git's own convention of naming a
// diff by its post-change path. Everything from a "@@ ... @@" hunk header up
// to (but not including) the next hunk header or the next "diff --git" file
// boundary is kept, verbatim, as one entry in that file's Hunks —
// SanityCheck only ever needs to check whether an evidence citation's text
// is a substring of one of these, not fully re-parse diff semantics.
func ParseUnifiedDiff(diff string) []DiffFile {
	var files []DiffFile
	var current *DiffFile
	var hunk strings.Builder
	hunkOpen := false
	var oldPath string

	flushHunk := func() {
		if hunkOpen && current != nil {
			current.Hunks = append(current.Hunks, hunk.String())
		}
		hunk.Reset()
		hunkOpen = false
	}

	for _, line := range strings.Split(diff, "\n") {
		switch {
		case strings.HasPrefix(line, "diff --git "):
			flushHunk()
			files = append(files, DiffFile{})
			current = &files[len(files)-1]
			oldPath = ""
		case strings.HasPrefix(line, "--- "):
			oldPath = strings.TrimPrefix(strings.TrimPrefix(line, "--- "), "a/")
		case strings.HasPrefix(line, "+++ "):
			if current != nil {
				newPath := strings.TrimPrefix(strings.TrimPrefix(line, "+++ "), "b/")
				if newPath == "/dev/null" && oldPath != "" && oldPath != "/dev/null" {
					current.Path = oldPath
				} else {
					current.Path = newPath
				}
			}
		case strings.HasPrefix(line, "@@ "):
			flushHunk()
			hunkOpen = true
			hunk.WriteString(line)
		case hunkOpen:
			hunk.WriteString("\n")
			hunk.WriteString(line)
		}
	}
	flushHunk()

	return files
}
