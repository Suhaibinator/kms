package envinject

import (
	"encoding/base64"
	"slices"
	"strings"
	"testing"
)

func TestMapKey(t *testing.T) {
	t.Parallel()
	cases := []struct {
		key  string
		want string
	}{
		{"x", "X"},
		{"billing/stripe-key", "BILLING_STRIPE_KEY"},
		{"a/b/c/d", "A_B_C_D"},
		{"a-b", "A_B"},
		{"a.b", "A_B"},
		{"a_b", "A_B"},
		{"log.format", "LOG_FORMAT"},
		{"a_b.c-d/e_f", "A_B_C_D_E_F"},
		{"db2", "DB2"},
		{"a1/b2.c3-d4", "A1_B2_C3_D4"},
	}
	for _, tc := range cases {
		got, err := MapKey(tc.key)
		if err != nil {
			t.Errorf("MapKey(%q) unexpected error: %v", tc.key, err)
			continue
		}
		if got != tc.want {
			t.Errorf("MapKey(%q) = %q, want %q", tc.key, got, tc.want)
		}
	}
}

func TestMapKeyInvalid(t *testing.T) {
	t.Parallel()
	invalid := []string{
		"",
		"/leading",
		"trailing/",
		"a//b",
		"a/./b",
		"a/../b",
		"UP/case",
		"a b",
		"a*",
		"a\x00",
		"a/ünicode",
	}
	for _, key := range invalid {
		if got, err := MapKey(key); err == nil {
			t.Errorf("MapKey(%q) = %q, want error", key, got)
		}
	}
}

func TestMapKeyLeadingDigitSuggestsPrefix(t *testing.T) {
	t.Parallel()
	_, err := MapKey("9lives/cat")
	if err == nil {
		t.Fatal("MapKey(\"9lives/cat\") = nil error, want error")
	}
	if !strings.Contains(err.Error(), "--env-prefix") {
		t.Errorf("error %q does not mention --env-prefix", err)
	}
	if !strings.Contains(err.Error(), "9LIVES_CAT") {
		t.Errorf("error %q does not name the mapped variable", err)
	}
}

func TestMapAlias(t *testing.T) {
	t.Parallel()
	cases := []struct {
		alias string
		want  string
	}{
		{"a", "A"},
		{"stripe-key", "STRIPE_KEY"},
		{"Stripe_Key", "STRIPE_KEY"},
		{"a-b-c", "A_B_C"},
		{"db2", "DB2"},
		{"x1_y2-z3", "X1_Y2_Z3"},
	}
	for _, tc := range cases {
		got, err := MapAlias(tc.alias)
		if err != nil {
			t.Errorf("MapAlias(%q) unexpected error: %v", tc.alias, err)
			continue
		}
		if got != tc.want {
			t.Errorf("MapAlias(%q) = %q, want %q", tc.alias, got, tc.want)
		}
	}
}

func TestMapAliasInvalid(t *testing.T) {
	t.Parallel()
	invalid := []string{
		"",
		"1abc", // must start with a letter
		"-abc", // must start with a letter
		"_abc", // must start with a letter
		"a.b",  // dots are not alias characters
		"a/b",  // slashes are not alias characters
		"a b",  // spaces are not alias characters
		"ünicode",
	}
	for _, alias := range invalid {
		if got, err := MapAlias(alias); err == nil {
			t.Errorf("MapAlias(%q) = %q, want error", alias, got)
		}
	}
}

func TestValidPrefix(t *testing.T) {
	t.Parallel()
	cases := []struct {
		prefix string
		want   bool
	}{
		{"", true}, // no prefix
		{"APP_", true},
		{"_", true},
		{"_x9", true},
		{"a", true},
		{"A1_B2", true},
		{"1APP", false},
		{"APP-", false},
		{"APP.", false},
		{"APP/", false},
		{"APP ", false},
		{"ÄPP", false},
	}
	for _, tc := range cases {
		if got := ValidPrefix(tc.prefix); got != tc.want {
			t.Errorf("ValidPrefix(%q) = %v, want %v", tc.prefix, got, tc.want)
		}
	}
}

func TestIsBinary(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		value []byte
		want  bool
	}{
		{"nil", nil, false},
		{"empty", []byte{}, false},
		{"ascii", []byte("hello world"), false},
		{"multibyte utf8", []byte("héllo ✓ 🔐"), false},
		{"base64 text", []byte("aGVsbG8gd29ybGQ="), false},
		{"newlines", []byte("line1\nline2\n"), false},
		{"nul byte", []byte("a\x00b"), true},
		{"trailing nul", []byte("abc\x00"), true},
		{"invalid utf8", []byte{0xff, 0xfe, 0x41}, true},
		{"truncated rune", []byte("h\xc3"), true},
	}
	for _, tc := range cases {
		if got := IsBinary(tc.value); got != tc.want {
			t.Errorf("IsBinary(%s) = %v, want %v", tc.name, got, tc.want)
		}
	}
}

func TestResolveSortsAndKeepsSecretFlag(t *testing.T) {
	t.Parallel()
	items := []Item{
		{Key: "zeta", Value: []byte("z"), Secret: true},
		{Key: "billing/stripe-key", Value: []byte("sk_live"), Secret: true},
		{Key: "log.format", Value: []byte("json")},
	}
	vars, notes, err := Resolve(items, Rules{})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(notes) != 0 {
		t.Errorf("notes = %v, want none", notes)
	}
	want := []Var{
		{Name: "BILLING_STRIPE_KEY", Value: "sk_live", Secret: true},
		{Name: "LOG_FORMAT", Value: "json"},
		{Name: "ZETA", Value: "z", Secret: true},
	}
	if !slices.Equal(vars, want) {
		t.Errorf("vars = %+v, want %+v", vars, want)
	}
}

func TestResolveAliasWins(t *testing.T) {
	t.Parallel()
	vars, _, err := Resolve([]Item{{Key: "ignored/key", Alias: "stripe-key", Value: []byte("v")}}, Rules{})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(vars) != 1 || vars[0].Name != "STRIPE_KEY" {
		t.Errorf("vars = %+v, want a single STRIPE_KEY", vars)
	}
}

func TestResolvePrefix(t *testing.T) {
	t.Parallel()
	items := []Item{
		{Key: "9lives", Value: []byte("cat")},
		{Key: "db", Value: []byte("pg")},
	}
	// Without a prefix the leading digit is fatal and the message says so.
	if _, _, err := Resolve(items, Rules{}); err == nil {
		t.Error("Resolve without prefix = nil error, want error")
	} else if !strings.Contains(err.Error(), "--env-prefix") {
		t.Errorf("error %q does not mention --env-prefix", err)
	}
	// With a prefix the name is legal again; the prefix is prepended verbatim.
	vars, _, err := Resolve(items, Rules{Prefix: "APP_"})
	if err != nil {
		t.Fatalf("Resolve with prefix: %v", err)
	}
	want := []Var{{Name: "APP_9LIVES", Value: "cat"}, {Name: "APP_DB", Value: "pg"}}
	if !slices.Equal(vars, want) {
		t.Errorf("vars = %+v, want %+v", vars, want)
	}
	// No separator is inserted for the caller.
	vars, _, err = Resolve([]Item{{Key: "db", Value: []byte("pg")}}, Rules{Prefix: "APP"})
	if err != nil {
		t.Fatalf("Resolve with bare prefix: %v", err)
	}
	if vars[0].Name != "APPDB" {
		t.Errorf("name = %q, want APPDB", vars[0].Name)
	}
}

func TestResolveInvalidPrefix(t *testing.T) {
	t.Parallel()
	for _, prefix := range []string{"1APP", "APP-", "APP ", "APP."} {
		if _, _, err := Resolve([]Item{{Key: "db", Value: []byte("pg")}}, Rules{Prefix: prefix}); err == nil {
			t.Errorf("Resolve with prefix %q = nil error, want error", prefix)
		}
	}
}

func TestResolveBinaryValue(t *testing.T) {
	t.Parallel()
	raw := []byte{0x00, 0x01, 0xff, 0xfe, 'a'}
	items := []Item{
		{Key: "cert/der", Value: raw, Secret: true},
		{Key: "cert/pem", Value: []byte("-----BEGIN-----"), Secret: true},
	}
	vars, notes, err := Resolve(items, Rules{})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	want := []Var{
		{Name: "CERT_DER_B64", Value: base64.StdEncoding.EncodeToString(raw), Secret: true},
		{Name: "CERT_PEM", Value: "-----BEGIN-----", Secret: true},
	}
	if !slices.Equal(vars, want) {
		t.Errorf("vars = %+v, want %+v", vars, want)
	}
	wantNotes := []Note{{Kind: NoteBinaryEncoded, Key: "cert/der", Name: "CERT_DER_B64"}}
	if !slices.Equal(notes, wantNotes) {
		t.Errorf("notes = %+v, want %+v", notes, wantNotes)
	}
	// The encoding is standard and padded, so it decodes back byte for byte.
	got, err := base64.StdEncoding.DecodeString(vars[0].Value)
	if err != nil {
		t.Fatalf("DecodeString: %v", err)
	}
	if !slices.Equal(got, raw) {
		t.Errorf("decoded = %v, want %v", got, raw)
	}
}

func TestResolveBinaryValueWithPrefix(t *testing.T) {
	t.Parallel()
	vars, notes, err := Resolve([]Item{{Key: "blob", Value: []byte{0xff}}}, Rules{Prefix: "APP_"})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if vars[0].Name != "APP_BLOB_B64" {
		t.Errorf("name = %q, want APP_BLOB_B64", vars[0].Name)
	}
	if len(notes) != 1 || notes[0].Name != "APP_BLOB_B64" || notes[0].Key != "blob" {
		t.Errorf("notes = %+v, want one note for blob", notes)
	}
}

func TestResolveCollisions(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		items   []Item
		sources [2]string
		varName string
	}{
		{
			name:    "dash versus underscore",
			items:   []Item{{Key: "a-b", Value: []byte("1")}, {Key: "a_b", Value: []byte("2")}},
			sources: [2]string{"a-b", "a_b"},
			varName: "A_B",
		},
		{
			name:    "dot versus slash",
			items:   []Item{{Key: "a.b", Value: []byte("1")}, {Key: "a/b", Value: []byte("2")}},
			sources: [2]string{"a.b", "a/b"},
			varName: "A_B",
		},
		{
			name:    "alias dash versus underscore",
			items:   []Item{{Alias: "foo-bar", Value: []byte("1")}, {Alias: "foo_bar", Value: []byte("2")}},
			sources: [2]string{"foo-bar", "foo_bar"},
			varName: "FOO_BAR",
		},
		{
			name:    "text name versus base64 suffix",
			items:   []Item{{Key: "x_b64", Value: []byte("text")}, {Key: "x", Value: []byte{0x00}}},
			sources: [2]string{"x_b64", "x"},
			varName: "X_B64",
		},
		{
			name:    "same key twice",
			items:   []Item{{Key: "dup", Value: []byte("1")}, {Key: "dup", Value: []byte("2")}},
			sources: [2]string{"dup", "dup"},
			varName: "DUP",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, _, err := Resolve(tc.items, Rules{})
			if err == nil {
				t.Fatal("Resolve = nil error, want collision error")
			}
			msg := err.Error()
			for _, source := range tc.sources {
				if !strings.Contains(msg, source) {
					t.Errorf("error %q does not name source %q", msg, source)
				}
			}
			if !strings.Contains(msg, tc.varName) {
				t.Errorf("error %q does not name variable %q", msg, tc.varName)
			}
		})
	}
}

func TestResolveEntryCap(t *testing.T) {
	t.Parallel()
	secret := strings.Repeat("s", 64)
	items := []Item{
		{Key: "small", Value: []byte("v")},
		{Key: "big", Value: []byte(secret), Secret: true},
	}
	// BIG=<64 bytes> is 68 bytes; a 32-byte cap rejects it.
	_, _, err := Resolve(items, Rules{MaxEntryBytes: 32})
	if err == nil {
		t.Fatal("Resolve = nil error, want entry cap error")
	}
	if !strings.Contains(err.Error(), "BIG") {
		t.Errorf("error %q does not name the offending variable", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("error %q leaks the value", err)
	}
	// The cap counts name + '=' + value, so an exactly-sized entry passes.
	exact := len("BIG") + 1 + len(secret)
	if _, _, err := Resolve(items, Rules{MaxEntryBytes: exact}); err != nil {
		t.Errorf("Resolve at exactly the cap: %v", err)
	}
	if _, _, err := Resolve(items, Rules{MaxEntryBytes: exact - 1}); err == nil {
		t.Error("Resolve one byte over the cap = nil error, want error")
	}
}

func TestResolveTotalCap(t *testing.T) {
	t.Parallel()
	secret := strings.Repeat("s", 32)
	items := []Item{
		{Key: "a", Value: []byte(secret), Secret: true},
		{Key: "b", Value: []byte(secret), Secret: true},
	}
	// Each entry is len("A")+1+32 = 34 bytes, so the total is 68.
	_, _, err := Resolve(items, Rules{MaxTotalBytes: 67})
	if err == nil {
		t.Fatal("Resolve = nil error, want total cap error")
	}
	if strings.Contains(err.Error(), secret) {
		t.Errorf("error %q leaks a value", err)
	}
	if !strings.Contains(err.Error(), "68") || !strings.Contains(err.Error(), "67") {
		t.Errorf("error %q does not report the total and the limit", err)
	}
	if _, _, err := Resolve(items, Rules{MaxTotalBytes: 68}); err != nil {
		t.Errorf("Resolve at exactly the total cap: %v", err)
	}
}

func TestResolveZeroCapsAreUnlimited(t *testing.T) {
	t.Parallel()
	big := strings.Repeat("x", 1<<20)
	items := []Item{{Key: "a", Value: []byte(big)}, {Key: "b", Value: []byte(big)}}
	vars, _, err := Resolve(items, Rules{})
	if err != nil {
		t.Fatalf("Resolve with zero caps: %v", err)
	}
	if len(vars) != 2 {
		t.Errorf("len(vars) = %d, want 2", len(vars))
	}
}

func TestResolveEmpty(t *testing.T) {
	t.Parallel()
	vars, notes, err := Resolve(nil, Rules{MaxEntryBytes: 8, MaxTotalBytes: 8})
	if err != nil {
		t.Fatalf("Resolve(nil): %v", err)
	}
	if len(vars) != 0 || len(notes) != 0 {
		t.Errorf("Resolve(nil) = %+v, %+v, want empty", vars, notes)
	}
}

func TestResolveRejectsItemWithoutName(t *testing.T) {
	t.Parallel()
	if _, _, err := Resolve([]Item{{Value: []byte("v")}}, Rules{}); err == nil {
		t.Error("Resolve with neither key nor alias = nil error, want error")
	}
}

func TestMergeInjectedWins(t *testing.T) {
	t.Parallel()
	parent := []string{"PATH=/bin", "A=parent", "KMS_SECRET_TOKEN_PROD=tok", "HOME=/root"}
	vars := []Var{{Name: "B", Value: "new"}, {Name: "A", Value: "injected", Secret: true}}
	env, shadowed := Merge(parent, vars, false, false)
	want := []string{"PATH=/bin", "HOME=/root", "A=injected", "B=new"}
	if !slices.Equal(env, want) {
		t.Errorf("env = %q, want %q", env, want)
	}
	if len(shadowed) != 0 {
		t.Errorf("shadowed = %q, want none", shadowed)
	}
}

func TestMergePreserveParent(t *testing.T) {
	t.Parallel()
	parent := []string{"PATH=/bin", "A=parent", "C=parent", "KMS_SECRET_TOKEN_PROD=tok"}
	vars := []Var{{Name: "C", Value: "injected"}, {Name: "B", Value: "new"}, {Name: "A", Value: "injected"}}
	env, shadowed := Merge(parent, vars, true, false)
	want := []string{"PATH=/bin", "A=parent", "C=parent", "B=new"}
	if !slices.Equal(env, want) {
		t.Errorf("env = %q, want %q", env, want)
	}
	if !slices.Equal(shadowed, []string{"A", "C"}) {
		t.Errorf("shadowed = %q, want [A C]", shadowed)
	}
}

func TestMergeStripsTokenVariables(t *testing.T) {
	t.Parallel()
	parent := []string{
		"KMS_SECRET_TOKEN_PROD=tok",
		"KMS_SECRET_TOKEN_=tok",
		"kms_secret_token_prod=tok",
		"KMS_SECRET_TOKENX=keep",
		"KMS_TOKEN=keep",
	}
	for _, preserve := range []bool{false, true} {
		env, _ := Merge(parent, nil, preserve, false)
		want := []string{"kms_secret_token_prod=tok", "KMS_SECRET_TOKENX=keep", "KMS_TOKEN=keep"}
		if !slices.Equal(env, want) {
			t.Errorf("preserveParent=%v: env = %q, want %q", preserve, env, want)
		}
	}
	// Windows compares names without regard to case, so the lowercase spelling
	// is a token variable too.
	env, _ := Merge(parent, nil, false, true)
	want := []string{"KMS_SECRET_TOKENX=keep", "KMS_TOKEN=keep"}
	if !slices.Equal(env, want) {
		t.Errorf("caseInsensitive: env = %q, want %q", env, want)
	}
}

func TestMergeCaseInsensitiveInjectedWins(t *testing.T) {
	t.Parallel()
	parent := []string{"Path=C:\\Windows", "path=C:\\dup", "Home=C:\\Users\\me"}
	vars := []Var{{Name: "PATH", Value: "C:\\injected"}}
	env, shadowed := Merge(parent, vars, false, true)
	// The parent's Path (and its case-differing duplicate) give way to the
	// injected spelling.
	want := []string{"Home=C:\\Users\\me", "PATH=C:\\injected"}
	if !slices.Equal(env, want) {
		t.Errorf("env = %q, want %q", env, want)
	}
	if len(shadowed) != 0 {
		t.Errorf("shadowed = %q, want none", shadowed)
	}
}

func TestMergeCaseInsensitivePreserveParent(t *testing.T) {
	t.Parallel()
	parent := []string{"Path=C:\\Windows", "path=C:\\dup"}
	vars := []Var{{Name: "PATH", Value: "C:\\injected"}, {Name: "New", Value: "1"}}
	env, shadowed := Merge(parent, vars, true, true)
	// The parent wins and keeps its own spelling; the duplicate is dropped.
	want := []string{"Path=C:\\Windows", "New=1"}
	if !slices.Equal(env, want) {
		t.Errorf("env = %q, want %q", env, want)
	}
	if !slices.Equal(shadowed, []string{"PATH"}) {
		t.Errorf("shadowed = %q, want [PATH]", shadowed)
	}
}

func TestMergeCaseSensitiveKeepsCaseDifferingNames(t *testing.T) {
	t.Parallel()
	parent := []string{"path=/lower", "PATH=/upper"}
	vars := []Var{{Name: "PATH", Value: "/injected"}}
	env, shadowed := Merge(parent, vars, false, false)
	want := []string{"path=/lower", "PATH=/injected"}
	if !slices.Equal(env, want) {
		t.Errorf("env = %q, want %q", env, want)
	}
	if len(shadowed) != 0 {
		t.Errorf("shadowed = %q, want none", shadowed)
	}
}

func TestMergeDropsMalformedParentEntries(t *testing.T) {
	t.Parallel()
	parent := []string{"NOEQUALS", "", "A=1", "=C:=C:\\dir", "B="}
	env, _ := Merge(parent, nil, false, false)
	want := []string{"A=1", "=C:=C:\\dir", "B="}
	if !slices.Equal(env, want) {
		t.Errorf("env = %q, want %q", env, want)
	}
}

func TestMergeDuplicateParentNames(t *testing.T) {
	t.Parallel()
	env, _ := Merge([]string{"A=first", "A=second", "B=1"}, nil, false, false)
	want := []string{"A=first", "B=1"}
	if !slices.Equal(env, want) {
		t.Errorf("env = %q, want %q", env, want)
	}
}

func TestMergeSortsInjectedAndDoesNotMutateInput(t *testing.T) {
	t.Parallel()
	vars := []Var{{Name: "Z", Value: "z"}, {Name: "A", Value: "a"}, {Name: "M", Value: "m"}}
	env, _ := Merge(nil, vars, false, false)
	want := []string{"A=a", "M=m", "Z=z"}
	if !slices.Equal(env, want) {
		t.Errorf("env = %q, want %q", env, want)
	}
	if vars[0].Name != "Z" {
		t.Errorf("Merge reordered the caller's slice: %+v", vars)
	}
}

func TestMergeDuplicateInjectedNames(t *testing.T) {
	t.Parallel()
	// Resolve never emits a duplicate, but Merge must still produce a
	// well-formed environment: the first name in sorted order wins.
	vars := []Var{{Name: "A", Value: "second"}, {Name: "A", Value: "first"}}
	env, _ := Merge(nil, vars, false, false)
	if !slices.Equal(env, []string{"A=second"}) {
		t.Errorf("env = %q, want [A=second]", env)
	}
	env, _ = Merge(nil, []Var{{Name: "a", Value: "lower"}, {Name: "A", Value: "upper"}}, false, true)
	if !slices.Equal(env, []string{"A=upper"}) {
		t.Errorf("env = %q, want [A=upper]", env)
	}
}

func TestMergeEmptyInputs(t *testing.T) {
	t.Parallel()
	env, shadowed := Merge(nil, nil, false, false)
	if len(env) != 0 || len(shadowed) != 0 {
		t.Errorf("Merge(nil, nil) = %q, %q, want empty", env, shadowed)
	}
}
