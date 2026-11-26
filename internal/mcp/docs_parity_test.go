package mcp

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
)

func registeredToolNames(t *testing.T) []string {
	t.Helper()
	s := NewServer(nil, nil, nil)
	var names []string
	for name := range s.MCP().ListTools() {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func readRepoFile(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile("../../" + rel)
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(b)
}

func TestREADMEListsEveryRegisteredTool(t *testing.T) {
	readme := readRepoFile(t, "README.md")
	var missing []string
	for _, name := range registeredToolNames(t) {
		if !strings.Contains(readme, "`"+name+"`") {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		t.Errorf("registered but absent from README: %v", missing)
	}
}

func TestREADMENamesOnlyRegisteredTools(t *testing.T) {
	readme := readRepoFile(t, "README.md")
	registered := map[string]bool{}
	for _, name := range registeredToolNames(t) {
		registered[name] = true
	}
	re := regexp.MustCompile("`([a-z][a-z0-9]*(?:_[a-z0-9]+){1,})`")
	known := map[string]bool{}
	for _, name := range registeredToolNames(t) {
		known[name] = true
	}
	allowed := map[string]bool{
		"status@broadcast": true, "s.whatsapp.net": true, "g.us": true,
		"whatsapp_web": true, "media_root": true, "store_dir": true,
		"read_only": true, "enabled_tools": true, "log_format": true,
		"full_history_sync": true, "allow_remote": true, "webhook_secret": true,
		"stt_endpoint": true, "stt_api_key": true, "stt_model": true,
		"stt_language": true, "device_name": true, "debug": true, "store": true,
		"addr": true, "mcp": true, "whatsapp-mcp": true,
		"xwa2_update_text_status": true, "uptime_sec": true, "close_friends": true,
		"admin_add": true, "all_member_add": true,
	}
	var ghosts []string
	for _, m := range re.FindAllStringSubmatch(readme, -1) {
		name := m[1]
		if registered[name] || allowed[name] || known[name] {
			continue
		}
		ghosts = append(ghosts, name)
	}
	if len(ghosts) > 0 {
		t.Errorf("README names identifiers that are not registered tools (fix the name or extend the allowlist): %v", ghosts)
	}
}

func TestToolCountMatchesEveryClaimingFile(t *testing.T) {
	want := len(registeredToolNames(t))
	cases := map[string]*regexp.Regexp{
		"README.md":          regexp.MustCompile(`exposes \*\*(\d+) tools\*\*`),
		"server.json":        regexp.MustCompile(`with (\d+) MCP tools`),
		"mcpb/manifest.json": regexp.MustCompile(`(\d+) tools over stdio`),
	}
	for file, re := range cases {
		m := re.FindStringSubmatch(readRepoFile(t, file))
		if m == nil {
			t.Errorf("%s: no tool-count claim found", file)
			continue
		}
		if got := m[1]; got != itoa(want) {
			t.Errorf("%s claims %s tools, server registers %d", file, got, want)
		}
	}
}

func TestREADMEGoVersionMatchesModule(t *testing.T) {
	gomod := readRepoFile(t, "go.mod")
	re := regexp.MustCompile(`(?m)^go (\d+\.\d+)`)
	m := re.FindStringSubmatch(gomod)
	if m == nil {
		t.Fatal("go.mod has no go directive")
	}
	want := "Go " + m[1]
	readme := readRepoFile(t, "README.md")
	if !strings.Contains(readme, want+"+") {
		t.Errorf("README must state %s+ to match go.mod", want)
	}
}

func TestEnumArgsMatchTheirDescriptions(t *testing.T) {
	s := NewServer(nil, nil, nil)
	tools := s.MCP().ListTools()
	cases := []struct{ tool, prop, values string }{
		{"set_privacy_setting", "name", "groupadd|readreceipts|calladd|last|profile|status|online|messages|stickers"},
		{"set_status_privacy_setting", "mode", "all|contacts|close_friends|custom"},
		{"send_typing", "kind", "audio"},
	}
	for _, tc := range cases {
		tool, ok := tools[tc.tool]
		if !ok {
			t.Errorf("tool %s not registered", tc.tool)
			continue
		}
		schema := tool.Tool.InputSchema
		raw, err := mcpSchemaProperty(schema, tc.prop)
		if err != nil {
			t.Errorf("%s.%s: %v", tc.tool, tc.prop, err)
			continue
		}
		for _, v := range strings.Split(tc.values, "|") {
			if v == "indexes" {
				continue
			}
			if !strings.Contains(raw, v) {
				t.Errorf("%s.%s: schema/description does not mention %q", tc.tool, tc.prop, v)
			}
		}
	}
}

func mcpSchemaProperty(schema mcp.ToolInputSchema, prop string) (string, error) {
	b, err := json.Marshal(schema)
	if err != nil {
		return "", err
	}
	var generic map[string]any
	if err := json.Unmarshal(b, &generic); err != nil {
		return "", err
	}
	props, _ := generic["properties"].(map[string]any)
	entry, ok := props[prop]
	if !ok {
		return "", fmt.Errorf("property not in schema")
	}
	out, err := json.Marshal(entry)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
