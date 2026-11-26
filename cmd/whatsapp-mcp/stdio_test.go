package main

import (
	"reflect"
	"testing"
)

func TestParseEnabledTools(t *testing.T) {
	tests := []struct {
		name      string
		flagValue string
		envValue  string
		want      []string
	}{
		{name: "none", want: nil},
		{name: "flag", flagValue: "a,b", envValue: "c,d", want: []string{"a", "b"}},
		{name: "env fallback", envValue: "list_chats, send_message", want: []string{"list_chats", "send_message"}},
		{name: "drops empties", flagValue: " list_chats , , send_message,", want: []string{"list_chats", "send_message"}},
		{name: "empty flag falls back", flagValue: " ", envValue: "x", want: []string{"x"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("WHATSAPP_MCP_ENABLED_TOOLS", tt.envValue)
			got := parseEnabledTools(tt.flagValue)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("parseEnabledTools(%q) = %#v, want %#v", tt.flagValue, got, tt.want)
			}
		})
	}
}

func TestReadOnlyFromEnv(t *testing.T) {
	for _, v := range []string{"1", "true", "TRUE", "yes", "YeS"} {
		t.Setenv("WHATSAPP_MCP_READ_ONLY", v)
		if !readOnlyFromEnv() {
			t.Fatalf("readOnlyFromEnv() false for %q", v)
		}
	}
	for _, v := range []string{"", "0", "false", "no"} {
		t.Setenv("WHATSAPP_MCP_READ_ONLY", v)
		if readOnlyFromEnv() {
			t.Fatalf("readOnlyFromEnv() true for %q", v)
		}
	}
}

func TestRunStdioRejectsUnknownFlag(t *testing.T) {
	if got := runStdio(t.TempDir(), nil, []string{"-not-a-flag"}); got != 2 {
		t.Fatalf("runStdio with unknown flag = %d, want 2", got)
	}
}
