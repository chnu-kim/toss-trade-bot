package gate

import (
	"strings"
	"testing"
)

const sampleUnifiedDiff = `diff --git a/internal/market/market.go b/internal/market/market.go
index 1111111..2222222 100644
--- a/internal/market/market.go
+++ b/internal/market/market.go
@@ -10,6 +10,7 @@ func Foo() {
 	line one
 	line two
+	added a nil check
 	line three
@@ -40,3 +41,4 @@ func Bar() {
 	tail one
+	added tail line
diff --git a/docs/README.md b/docs/README.md
index 3333333..4444444 100644
--- a/docs/README.md
+++ b/docs/README.md
@@ -1,2 +1,3 @@
 # Title
+extra line
`

func TestParseUnifiedDiff_SplitsFilesAndHunks(t *testing.T) {
	files := ParseUnifiedDiff(sampleUnifiedDiff)
	if len(files) != 2 {
		t.Fatalf("ParseUnifiedDiff() returned %d files, want 2: %+v", len(files), files)
	}
	if files[0].Path != "internal/market/market.go" {
		t.Errorf("files[0].Path = %q, want internal/market/market.go", files[0].Path)
	}
	if len(files[0].Hunks) != 2 {
		t.Fatalf("files[0] has %d hunks, want 2", len(files[0].Hunks))
	}
	if !strings.Contains(files[0].Hunks[0], "added a nil check") {
		t.Errorf("files[0].Hunks[0] = %q, want it to contain the added line", files[0].Hunks[0])
	}
	if !strings.Contains(files[0].Hunks[1], "added tail line") {
		t.Errorf("files[0].Hunks[1] = %q, want it to contain the added tail line", files[0].Hunks[1])
	}

	if files[1].Path != "docs/README.md" {
		t.Errorf("files[1].Path = %q, want docs/README.md", files[1].Path)
	}
	if len(files[1].Hunks) != 1 || !strings.Contains(files[1].Hunks[0], "extra line") {
		t.Errorf("files[1].Hunks = %+v, want one hunk containing 'extra line'", files[1].Hunks)
	}
}

func TestParseUnifiedDiff_EmptyInput(t *testing.T) {
	if files := ParseUnifiedDiff(""); len(files) != 0 {
		t.Errorf("ParseUnifiedDiff(\"\") = %+v, want empty", files)
	}
}

func TestParseUnifiedDiff_NewFileNoLeadingIndexLine(t *testing.T) {
	diff := `diff --git a/new.go b/new.go
new file mode 100644
index 0000000..1111111
--- /dev/null
+++ b/new.go
@@ -0,0 +1,2 @@
+package gate
+// new file
`
	files := ParseUnifiedDiff(diff)
	if len(files) != 1 {
		t.Fatalf("ParseUnifiedDiff() returned %d files, want 1", len(files))
	}
	if files[0].Path != "new.go" {
		t.Errorf("files[0].Path = %q, want new.go", files[0].Path)
	}
}
