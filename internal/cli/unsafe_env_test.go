package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	kmsv1 "github.com/Suhaibinator/kms/gen/kmsv1"
)

func unsafeEnvFixture(f *envFixture, release bool) []string {
	if release {
		f.installRelease()
		f.releases.release.Entries[0].Alias = "node-options"
		setEnvTestReleaseDigest(f.releases.release)
		return []string{"--release", "runtime"}
	}
	f.params.list[0].Ref.Key = "node_options"
	return nil
}

func TestExecUnsafeNames(t *testing.T) {
	for _, release := range []bool{false, true} {
		for _, mode := range []string{"deny", "prefix", "override", "preserve-deny", "preserve-override"} {
			t.Run(mode+map[bool]string{false: "-namespace", true: "-release"}[release], func(t *testing.T) {
				f := newExecFixture(t, 0, nil)
				args := unsafeEnvFixture(f.envFixture, release)
				parent := f.environOverride
				f.environOverride = func() []string { return append(parent(), "NODE_OPTIONS=parent-options") }
				switch mode {
				case "prefix":
					args = append(args, "--env-prefix", "MYAPP_")
				case "override":
					args = append(args, "--allow-unsafe-env-names")
				case "preserve-deny":
					args = append(args, "--preserve-env")
				case "preserve-override":
					args = append(args, "--preserve-env", "--allow-unsafe-env-names")
				}
				denied := strings.HasSuffix(mode, "deny")
				code := f.runExec(args, "/usr/bin/app")
				if denied {
					if code == 0 || f.launched.called || !strings.Contains(f.stderr(), "NODE_OPTIONS") {
						t.Fatalf("unsafe launch: code %d, launched %v, %s", code, f.launched.called, f.stderr())
					}
					return
				}
				if code != 0 || !f.launched.called {
					t.Fatalf("safe launch refused: %s", f.stderr())
				}
				wantName := "NODE_OPTIONS"
				if mode == "prefix" {
					wantName = "MYAPP_NODE_OPTIONS"
				}
				found := false
				for _, entry := range f.launched.env {
					name, _, _ := strings.Cut(entry, "=")
					if name == wantName {
						if mode == "preserve-override" && entry != "NODE_OPTIONS=parent-options" {
							t.Fatal("parent precedence lost")
						}
						found = true
					}
					if name == bindingKeyEnv || name == newBindingKeyEnv {
						t.Fatal("binding credential leaked")
					}
				}
				if !found {
					t.Fatalf("%s missing", wantName)
				}
			})
		}
	}
}

func TestEnvUnsafeNames(t *testing.T) {
	for _, release := range []bool{false, true} {
		for _, format := range []string{"dotenv", "export", "json", "yaml"} {
			for _, mode := range []string{"deny", "prefix", "override"} {
				t.Run(format+"-"+mode+map[bool]string{false: "-namespace", true: "-release"}[release], func(t *testing.T) {
					f := newEnvFixture(t)
					args := append(unsafeEnvFixture(f, release), "--format", format)
					if mode == "prefix" {
						args = append(args, "--env-prefix", "MYAPP_")
					}
					if mode == "override" {
						args = append(args, "--allow-unsafe-env-names")
					}
					code := f.run(args...)
					if mode == "deny" {
						if code == 0 || f.stdout() != "" || !strings.Contains(f.stderr(), "NODE_OPTIONS") {
							t.Fatalf("unsafe output: %q, %s", f.stdout(), f.stderr())
						}
					} else if code != 0 || !strings.Contains(f.stdout(), "NODE_OPTIONS") {
						t.Fatalf("output refused: %s", f.stderr())
					}
				})
			}
		}
	}
}

func TestEnvUnsafeOutputLeavesFilesUntouched(t *testing.T) {
	for _, badValue := range []bool{false, true} {
		for _, existing := range []bool{false, true} {
			for _, force := range []bool{false, true} {
				f := newEnvFixture(t)
				if badValue {
					f.params.list[0].Value = "private\uFEFFvalue"
				} else {
					unsafeEnvFixture(f, false)
				}
				path := filepath.Join(t.TempDir(), "env")
				if existing {
					if err := os.WriteFile(path, []byte("original"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				args := []string{"--out", path}
				if force {
					args = append(args, "--force")
				}
				if f.run(args...) == 0 {
					t.Fatal("unsafe output succeeded")
				}
				// An existing destination without --force can fail earlier.
				if !existing || force {
					reason := "unsafe environment variable"
					if badValue {
						reason = "unsupported by dotenv"
					}
					if !strings.Contains(f.stderr(), reason) {
						t.Fatalf("wrong refusal: %s", f.stderr())
					}
				}
				data, err := os.ReadFile(path)
				if existing {
					if err != nil || string(data) != "original" {
						t.Fatalf("destination modified: %v", err)
					}
				} else if !os.IsNotExist(err) {
					t.Fatalf("destination created: %v", err)
				}
				if strings.Contains(f.stderr(), "private") {
					t.Fatal("value leaked")
				}
			}
		}
	}
}

func TestUnsafeOverrideStillScrubsInjectedCredentials(t *testing.T) {
	f := newExecFixture(t, 0, nil)
	f.params.list = append(f.params.list, &kmsv1.Parameter{Ref: envTestRef("prod", "app", strings.ToLower(bindingKeyEnv)), Value: "injected-credential"})
	if code := f.runExec([]string{"--allow-unsafe-env-names"}, "/usr/bin/app"); code != 0 {
		t.Fatal(f.stderr())
	}
	for _, entry := range f.launched.env {
		if strings.HasPrefix(entry, bindingKeyEnv+"=") || strings.HasPrefix(entry, newBindingKeyEnv+"=") {
			t.Fatal("credential injected")
		}
	}
}

func TestEnvUnavailableUnsafeSecretStillRejected(t *testing.T) {
	f := newExecFixture(t, 0, nil)
	f.secrets.list[0].Ref.Key = "node_options"
	f.secrets.list[0].Bound = true
	f.secrets.list[0].Versions[0].Bound = true
	if code := f.runExec([]string{"--allow-incomplete-secrets"}, "/usr/bin/app"); code == 0 || f.launched.called || !strings.Contains(f.stderr(), "NODE_OPTIONS") {
		t.Fatalf("unsafe omitted mapping accepted: %s", f.stderr())
	}
}
