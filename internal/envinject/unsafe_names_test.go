package envinject

import (
	"strings"
	"testing"
)

func TestUnsafeNames(t *testing.T) {
	// Independent list: omissions from the production policy must fail this test.
	names := strings.Fields(`PATH ENV BASH_ENV SHELLOPTS BASHOPTS IFS PS4 CDPATH
 GCONV_PATH HOSTALIASES LOCPATH NLSPATH RESOLV_HOST_CONF GLIBC_TUNABLES
 NODE_OPTIONS NODE_EXTRA_CA_CERTS PYTHONPATH PYTHONHOME PYTHONSTARTUP PYTHONEXECUTABLE
 PERL5OPT PERL5LIB PERLLIB RUBYOPT RUBYLIB CLASSPATH JAVA_TOOL_OPTIONS JDK_JAVA_OPTIONS _JAVA_OPTIONS
 DOTNET_STARTUP_HOOKS CORECLR_ENABLE_PROFILING CORECLR_PROFILER CORECLR_PROFILER_PATH
 COR_ENABLE_PROFILING COR_PROFILER COR_PROFILER_PATH DOTNET_ENABLE_PROFILING DOTNET_PROFILER DOTNET_PROFILER_PATH
 CORECLR_PROFILER_PATH_64 CORECLR_PROFILER_PATH_32 COR_PROFILER_PATH_64 DOTNET_PROFILER_PATH_ARM64
 GIT_SSH GIT_SSH_COMMAND GIT_EXTERNAL_DIFF GIT_PAGER LESSOPEN LESSCLOSE PAGER EDITOR VISUAL PATHEXT COMSPEC
 SSL_CERT_FILE SSL_CERT_DIR CURL_CA_BUNDLE REQUESTS_CA_BUNDLE OPENSSL_CONF OPENSSL_MODULES
 LD_PRELOAD LD_LIBRARY_PATH DYLD_INSERT_LIBRARIES BASH_FUNC_ATTACK LD_PRELOAD_B64`)
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			if !UnsafeName(name) || !UnsafeName(strings.ToLower(name)) {
				t.Fatal("name not denied")
			}
			item := Item{Key: strings.ToLower(name), Value: []byte("sensitive-value")}
			_, _, err := Resolve([]Item{item}, Rules{})
			if err == nil || !strings.Contains(err.Error(), name) || !strings.Contains(err.Error(), item.Key) || strings.Contains(err.Error(), "sensitive-value") {
				t.Fatalf("wrong error: %v", err)
			}
			for _, rules := range []Rules{{Prefix: "MYAPP_"}, {AllowUnsafeNames: true}} {
				if _, _, err := Resolve([]Item{item}, rules); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
	for _, name := range []string{"MYAPP_NODE_OPTIONS", "NODE_OPTIONS_B64", "LD", "CORECLR_PROFILER_PATHOLOGY", "DOTNET_PROFILER_PATHOLOGY"} {
		if UnsafeName(name) {
			t.Fatalf("safe name denied: %s", name)
		}
	}
}

func TestUnsafeNamesFinalMapping(t *testing.T) {
	for _, tc := range []struct {
		item   Item
		rules  Rules
		denied bool
	}{
		{Item{Key: "ld/preload"}, Rules{}, true},
		{Item{Key: "node.options"}, Rules{}, true},
		{Item{Alias: "Node-Options"}, Rules{}, true},
		{Item{Key: "preload"}, Rules{Prefix: "ld_"}, true},
		{Item{Key: "options"}, Rules{Prefix: "NODE_"}, true},
		{Item{Key: "ld/preload", Value: []byte{0}}, Rules{}, true},
		{Item{Key: "node_options", Value: []byte{0}}, Rules{}, false},
	} {
		_, _, err := Resolve([]Item{tc.item}, tc.rules)
		if (err != nil) != tc.denied {
			t.Fatalf("%+v: %v", tc, err)
		}
	}
}

func TestUnsafeOverrideRetainsValidation(t *testing.T) {
	for _, tc := range []struct {
		items []Item
		rules Rules
	}{
		{[]Item{{Key: "node-options"}, {Key: "node_options"}}, Rules{AllowUnsafeNames: true}},
		{[]Item{{Key: "node_options", Value: []byte("long value")}}, Rules{AllowUnsafeNames: true, MaxEntryBytes: 1}},
		{[]Item{{Key: "node_options"}}, Rules{AllowUnsafeNames: true, MaxTotalBytes: 1}},
		{[]Item{{Key: "node_options"}}, Rules{AllowUnsafeNames: true, Prefix: "!"}},
	} {
		if _, _, err := Resolve(tc.items, tc.rules); err == nil {
			t.Fatal("override bypassed validation")
		}
	}
}
