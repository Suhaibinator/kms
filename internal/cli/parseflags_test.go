package cli

import (
	"reflect"
	"testing"
)

func TestParseFlagsInterspersedAndTerminator(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantOK  bool
		wantPos []string
		wantEP  string
		wantIns bool
	}{
		{
			name:    "flags before positionals",
			args:    []string{"--endpoint", "h:1", "--insecure", "/p", "v"},
			wantOK:  true,
			wantPos: []string{"/p", "v"},
			wantEP:  "h:1",
			wantIns: true,
		},
		{
			name:    "flags after positionals (interspersed)",
			args:    []string{"/p", "v", "--endpoint", "h:2", "--insecure"},
			wantOK:  true,
			wantPos: []string{"/p", "v"},
			wantEP:  "h:2",
			wantIns: true,
		},
		{
			name:    "flags on both sides of positionals",
			args:    []string{"--endpoint", "h:3", "/p", "--insecure", "v"},
			wantOK:  true,
			wantPos: []string{"/p", "v"},
			wantEP:  "h:3",
			wantIns: true,
		},
		{
			name:    "terminator lets a dash value through",
			args:    []string{"/p", "--endpoint", "h:4", "--", "-5"},
			wantOK:  true,
			wantPos: []string{"/p", "-5"},
			wantEP:  "h:4",
		},
		{
			name:    "terminator with multiple trailing dash values",
			args:    []string{"--endpoint", "h:5", "/p", "--", "--not-a-flag", "-x"},
			wantOK:  true,
			wantPos: []string{"/p", "--not-a-flag", "-x"},
			wantEP:  "h:5",
		},
		{
			name:    "lone dash is a positional, not a hang",
			args:    []string{"/p", "-"},
			wantOK:  true,
			wantPos: []string{"/p", "-"},
			wantEP:  "default",
		},
		{
			name:    "lone dash with flags after",
			args:    []string{"-", "--endpoint", "h:6"},
			wantOK:  true,
			wantPos: []string{"-"},
			wantEP:  "h:6",
		},
		{
			name:   "unknown flag fails",
			args:   []string{"/p", "--nope"},
			wantOK: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newTestCLI()
			fs := c.newFlags("test")
			endpoint := fs.String("endpoint", "default", "")
			insecure := fs.Bool("insecure", false, "")
			_ = fs.String("token", "", "")

			ok := c.parseFlags(fs, tt.args)
			if ok != tt.wantOK {
				t.Fatalf("parseFlags ok = %v, want %v", ok, tt.wantOK)
			}
			if !tt.wantOK {
				return
			}
			if !reflect.DeepEqual(c.args(), tt.wantPos) {
				t.Errorf("positionals = %v, want %v", c.args(), tt.wantPos)
			}
			if *endpoint != tt.wantEP {
				t.Errorf("endpoint = %q, want %q", *endpoint, tt.wantEP)
			}
			if *insecure != tt.wantIns {
				t.Errorf("insecure = %v, want %v", *insecure, tt.wantIns)
			}
		})
	}
}
