package main

import "testing"

// Every command listed in commandFlags must have subcommand help text, and
// vice versa — `gw <cmd> --help` prints subUsage[cmd].
func TestSubUsageCoversAllCommands(t *testing.T) {
	for cmd := range commandFlags {
		if _, ok := subUsage[cmd]; !ok {
			t.Errorf("command %q has no subUsage help text", cmd)
		}
	}
	for cmd := range subUsage {
		if _, ok := commandFlags[cmd]; !ok {
			t.Errorf("subUsage %q is not a known command", cmd)
		}
	}
}

func TestValidateFlags(t *testing.T) {
	tests := []struct {
		name    string
		cmd     string
		args    []string
		allowed []string
		wantErr bool
	}{
		{"no args", "up", nil, []string{"-d", "--detach"}, false},
		{"accepted short flag", "up", []string{"-d"}, []string{"-d", "--detach"}, false},
		{"accepted long flag", "up", []string{"--detach"}, []string{"-d", "--detach"}, false},
		{"positional passes through", "proxy", []string{"stop"}, []string{"-d", "--detach"}, false},
		{"unknown flag rejected", "up", []string{"--bogus"}, []string{"-d", "--detach"}, true},
		{"unknown flag among valid", "up", []string{"-d", "--nope"}, []string{"-d", "--detach"}, true},
		{"no flags allowed, unknown given", "down", []string{"-x"}, nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateFlags(tt.cmd, tt.args, tt.allowed)
			if (err != nil) != tt.wantErr {
				t.Errorf("validateFlags(%q, %v) err = %v, wantErr = %v", tt.cmd, tt.args, err, tt.wantErr)
			}
		})
	}
}
