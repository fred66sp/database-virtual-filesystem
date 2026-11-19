package cli

import (
	"bytes"
	"path/filepath"
	"testing"
)

// treeEntryJSON mirrors the documented nested shape. A nil Children means the
// node was not expanded (depth budget), while a non-nil empty slice means the
// node was expanded and has no children.
type treeEntryJSON struct {
	Name     string           `json:"name"`
	Type     string           `json:"type"`
	Path     string           `json:"path"`
	Children *[]treeEntryJSON `json:"children"`
}

type treeJSON struct {
	Path    string          `json:"path"`
	Entries []treeEntryJSON `json:"entries"`
}

func entryByName(t *testing.T, entries []treeEntryJSON, name string) treeEntryJSON {
	t.Helper()
	for _, e := range entries {
		if e.Name == name {
			return e
		}
	}
	t.Fatalf("entry %q not found in %+v", name, entries)
	return treeEntryJSON{}
}

func TestWP02Tree(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "config"))
	alpha := newFixture(t, alphaSchema)
	beta := newFixture(t, betaSchema)
	for _, m := range []struct{ file, alias string }{{alpha, "/alpha"}, {beta, "/beta"}} {
		if _, code := runCLI(t, "mount", m.file, m.alias); code != 0 {
			t.Fatalf("mount %s exit = %d", m.alias, code)
		}
	}

	// Default depth is two levels below PATH: mounts and their tables at the
	// root, tables and their rows under a mount.
	env, code := runCLI(t, "tree")
	root := decode[treeJSON](t, mustOK(t, env, code, "tree"))
	if root.Path != "/" || len(root.Entries) != 2 {
		t.Fatalf("tree = %+v, want path / and both mounts", root)
	}
	if root.Entries[0].Name != "alpha" || root.Entries[0].Type != "mount" ||
		root.Entries[1].Name != "beta" || root.Entries[1].Type != "mount" {
		t.Errorf("tree mounts = %+v, want alpha then beta", root.Entries)
	}
	alphaNode := root.Entries[0]
	if alphaNode.Children == nil || len(*alphaNode.Children) != 4 {
		t.Fatalf("tree / alpha children = %+v, want four tables", alphaNode.Children)
	}
	for _, table := range *alphaNode.Children {
		if table.Type != "table" || table.Children != nil {
			t.Errorf("tree / default table %+v, want a table without deeper levels", table)
		}
	}

	env, code = runCLI(t, "tree", "--depth", "1")
	root = decode[treeJSON](t, mustOK(t, env, code, "tree", "--depth", "1"))
	for _, e := range root.Entries {
		if e.Children != nil {
			t.Errorf("tree --depth 1 entry %+v, want no children", e)
		}
	}

	// A non-navigable table is an expanded empty container, not an error.
	env, code = runCLI(t, "tree", "/alpha", "--depth", "2")
	alphaTree := decode[treeJSON](t, mustOK(t, env, code, "tree", "/alpha", "--depth", "2"))
	if len(alphaTree.Entries) != 4 {
		t.Fatalf("tree /alpha = %+v, want four tables", alphaTree)
	}
	nopk := entryByName(t, alphaTree.Entries, "nopk")
	if nopk.Children == nil || len(*nopk.Children) != 0 {
		t.Errorf("tree non-navigable table = %+v, want children []", nopk)
	}
	tasks := entryByName(t, alphaTree.Entries, "tasks")
	if tasks.Children == nil || len(*tasks.Children) != 2 {
		t.Fatalf("tree tasks children = %+v, want rows 1 and 2", tasks.Children)
	}
	if (*tasks.Children)[0].Name != "1" || (*tasks.Children)[0].Type != "row" ||
		(*tasks.Children)[0].Path != "/alpha/tasks/1" || (*tasks.Children)[0].Children != nil {
		t.Errorf("tree tasks row = %+v, want /alpha/tasks/1 without fields at depth 2", (*tasks.Children)[0])
	}

	// From a table, depth 2 lists rows and their field keys.
	env, code = runCLI(t, "tree", "/alpha/tasks", "--depth", "2")
	rows := decode[treeJSON](t, mustOK(t, env, code, "tree", "/alpha/tasks", "--depth", "2"))
	if rows.Path != "/alpha/tasks" || len(rows.Entries) != 2 {
		t.Fatalf("tree /alpha/tasks = %+v, want the two rows", rows)
	}
	first := rows.Entries[0]
	if first.Children == nil || len(*first.Children) != 7 {
		t.Fatalf("tree row fields = %+v, want seven fields", first.Children)
	}
	if (*first.Children)[0].Type != "field" || (*first.Children)[0].Path != "/alpha/tasks/1/id" {
		t.Errorf("tree first field = %+v, want /alpha/tasks/1/id", (*first.Children)[0])
	}

	// A row path with the default depth lists its fields.
	env, code = runCLI(t, "tree", "/alpha/tasks/1")
	fields := decode[treeJSON](t, mustOK(t, env, code, "tree", "/alpha/tasks/1"))
	if len(fields.Entries) != 7 || fields.Entries[0].Type != "field" {
		t.Errorf("tree row = %+v, want the seven fields", fields)
	}

	// Budget exhaustion is reported honestly at every level.
	env, code = runCLI(t, "tree", "/alpha/tasks", "--depth", "1", "--limit", "1")
	limited := decode[treeJSON](t, mustOK(t, env, code, "tree", "/alpha/tasks", "--depth", "1", "--limit", "1"))
	if len(limited.Entries) != 1 || !env.Meta.Truncated {
		t.Errorf("tree --limit 1 = %+v, truncated %v; want one row and truncated true", limited, env.Meta.Truncated)
	}
	env, code = runCLI(t, "tree", "--limit", "1")
	limitedRoot := decode[treeJSON](t, mustOK(t, env, code, "tree", "--limit", "1"))
	if len(limitedRoot.Entries) != 1 || !env.Meta.Truncated {
		t.Errorf("tree root --limit 1 = %+v, truncated %v; want one mount and truncated true", limitedRoot, env.Meta.Truncated)
	}

	// Tree from the non-navigable table itself is an empty container.
	env, code = runCLI(t, "tree", "/alpha/nopk")
	empty := decode[treeJSON](t, mustOK(t, env, code, "tree", "/alpha/nopk"))
	if empty.Path != "/alpha/nopk" || len(empty.Entries) != 0 || env.Meta.Truncated {
		t.Errorf("tree /alpha/nopk = %+v, truncated %v; want an empty container", empty, env.Meta.Truncated)
	}

	env, code = runCLI(t, "tree", "/nowhere")
	mustCode(t, env, code, CodeNotFound, "tree", "/nowhere")

	// Output is deterministic: two identical invocations produce identical bytes.
	run := func(args ...string) string {
		t.Helper()
		var stdout, stderr bytes.Buffer
		if code := Run(args, &stdout, &stderr); code != 0 {
			t.Fatalf("Run(%q) exit = %d, stderr %q", args, code, stderr.String())
		}
		return stdout.String()
	}
	if firstRun, secondRun := run("tree", "/alpha", "--depth", "3", "--request-id", "fixed"), run("tree", "/alpha", "--depth", "3", "--request-id", "fixed"); firstRun != secondRun {
		t.Errorf("tree output is not deterministic:\n%s\n%s", firstRun, secondRun)
	}
}
