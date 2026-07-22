package integration

import (
	"errors"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/Suhaibinator/kms/sdk/go/kmsclient"
	"github.com/Suhaibinator/kms/sdk/go/kmsclient/kmsclienttest"
)

// TestKMSClientPackageBoundaryExcludesLegacyGoSDK locks in the intentional
// pre-release breaking rename. It catches both an accidentally restored
// compatibility directory and less obvious stale imports/package declarations
// that can otherwise survive when only the currently built platform is tested.
func TestKMSClientPackageBoundaryExcludesLegacyGoSDK(t *testing.T) {
	// Referencing both packages without import aliases verifies their public
	// package declarations match the documented import path names.
	_ = kmsclient.NewClient
	_ = kmsclienttest.New

	repositoryRoot := packageBoundaryRepositoryRoot(t)
	legacyPackage := "param" + "store"
	legacyHelperPackage := legacyPackage + "test"
	legacyImport := "github.com/Suhaibinator/kms/sdk/go/" + legacyPackage
	legacyDirectory := filepath.Join(repositoryRoot, "sdk", "go", legacyPackage)
	if _, err := os.Stat(legacyDirectory); !errors.Is(err, fs.ErrNotExist) {
		if err != nil {
			t.Fatalf("inspect removed Go SDK directory: %v", err)
		}
		t.Fatalf("removed Go SDK directory was restored: %s", legacyDirectory)
	}

	err := filepath.WalkDir(repositoryRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", "node_modules", "vendor":
				return fs.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" {
			return nil
		}

		parsed, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		switch parsed.Name.Name {
		case legacyPackage, legacyPackage + "_test", legacyHelperPackage, legacyHelperPackage + "_test":
			t.Errorf("legacy Go package declaration %q remains in %s", parsed.Name.Name, path)
		}
		for _, imported := range parsed.Imports {
			importPath, err := strconv.Unquote(imported.Path.Value)
			if err != nil {
				return err
			}
			if importPath == legacyImport || strings.HasPrefix(importPath, legacyImport+"/") {
				t.Errorf("legacy Go SDK import %q remains in %s", importPath, path)
			}
			if imported.Name != nil && (imported.Name.Name == legacyPackage || imported.Name.Name == legacyHelperPackage) {
				t.Errorf("legacy Go SDK import alias %q remains in %s", imported.Name.Name, path)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("inspect Go package boundary: %v", err)
	}
}

func packageBoundaryRepositoryRoot(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate package-boundary test source")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(filename), "..", ".."))
}
