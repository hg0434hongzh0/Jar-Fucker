package vscodeaudit

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestCreateWorkspaceAddsSourceRootsAndSiblingJars(t *testing.T) {
	root := t.TempDir()
	output := filepath.Join(root, "decompiled")
	source := filepath.Join(root, "libs")
	for _, dir := range []string{
		filepath.Join(output, "app", "com", "example"),
		filepath.Join(output, "dep", "org", "demo"),
		filepath.Join(source, "nested"),
	} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
	}
	appJar := filepath.Join(source, "app.jar")
	depJar := filepath.Join(source, "nested", "dep.jar")
	for _, file := range []string{appJar, depJar} {
		if err := os.WriteFile(file, []byte("jar"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	result, err := Create(Options{OutputDir: output, Jars: []string{appJar, depJar}, SourceDir: source})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.SourceRoots) != 2 || result.SourceRoots[0] != "./app" || result.SourceRoots[1] != "./dep" {
		t.Fatalf("source roots = %#v", result.SourceRoots)
	}
	if len(result.Libraries) != 2 {
		t.Fatalf("libraries = %#v", result.Libraries)
	}
	data, err := os.ReadFile(result.WorkspaceFile)
	if err != nil {
		t.Fatal(err)
	}
	var workspace map[string]any
	if err := json.Unmarshal(data, &workspace); err != nil {
		t.Fatal(err)
	}
	settings := workspace["settings"].(map[string]any)
	if _, ok := settings["java.project.sourcePaths"]; !ok {
		t.Fatal("missing sourcePaths")
	}
	if _, ok := settings["java.project.referencedLibraries"]; !ok {
		t.Fatal("missing referencedLibraries")
	}
}

func TestCreateWorkspaceSingleJarUsesOutputRoot(t *testing.T) {
	root := t.TempDir()
	output := filepath.Join(root, "out")
	if err := os.MkdirAll(output, 0755); err != nil {
		t.Fatal(err)
	}
	jarPath := filepath.Join(root, "one.jar")
	if err := os.WriteFile(jarPath, []byte("jar"), 0644); err != nil {
		t.Fatal(err)
	}
	result, err := Create(Options{OutputDir: output, Jars: []string{jarPath}})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.SourceRoots) != 1 || result.SourceRoots[0] != "." {
		t.Fatalf("source roots = %#v", result.SourceRoots)
	}
}
