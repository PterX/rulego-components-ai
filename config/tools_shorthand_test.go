package config

import (
	"encoding/json"
	"reflect"
	"testing"
)

// 字符串条目展开为按名解析(type 空),对象条目原样,可混用。
func TestToolUnmarshalShorthand(t *testing.T) {
	var tools []Tool
	in := `["bash", {"type":"rulechain","name":"清理任务","targetId":"tool_clean"}]`
	if err := json.Unmarshal([]byte(in), &tools); err != nil {
		t.Fatal(err)
	}
	if len(tools) != 2 {
		t.Fatalf("应解析 2 个条目: %d", len(tools))
	}
	if tools[0].Name != "bash" || tools[0].Type != "" {
		t.Fatalf("字符串条目应展开为 {name: bash, type: \"\"}: %+v", tools[0])
	}
	if tools[1].Type != ToolTypeRuleChain || tools[1].TargetId != "tool_clean" {
		t.Fatalf("对象条目应原样保留: %+v", tools[1])
	}
}

// 规整链配置 map 里的字符串条目(节点 Init 走 mapstructure,不经 json 钩子)。
func TestNormalizeToolsShorthand(t *testing.T) {
	cfg := map[string]interface{}{
		"model": "m",
		"tools": []interface{}{
			"bash",
			map[string]interface{}{"type": "rulechain", "name": "x", "targetId": "c1"},
			"",
		},
	}
	NormalizeToolsShorthand(cfg)
	list := cfg["tools"].([]interface{})
	first, ok := list[0].(map[string]interface{})
	if !ok || first["name"] != "bash" {
		t.Fatalf("字符串条目应规整为对象: %#v", list[0])
	}
	second := list[1].(map[string]interface{})
	if second["type"] != "rulechain" || second["targetId"] != "c1" {
		t.Fatalf("对象条目不应被改动: %#v", list[1])
	}
	if list[2] != "" {
		t.Fatalf("空串条目保持原样: %#v", list[2])
	}

	// 无 tools / 非数组 / nil map 均安全跳过。
	NormalizeToolsShorthand(nil)
	NormalizeToolsShorthand(map[string]interface{}{"tools": "not-a-list"})
	noTools := map[string]interface{}{"model": "m"}
	NormalizeToolsShorthand(noTools)
	if _, has := noTools["tools"]; has {
		t.Fatal("无 tools 不应新增字段")
	}
}

// 规整后再走 mapstructure 应得到与 json 直解一致的形状。
func TestNormalizeToolsShorthandMapStructEquivalence(t *testing.T) {
	raw := []interface{}{"read", "write"}
	cfg := map[string]interface{}{"tools": raw}
	NormalizeToolsShorthand(cfg)
	list := reflect.ValueOf(cfg["tools"])
	if list.Len() != 2 {
		t.Fatalf("条目数: %d", list.Len())
	}
	for i := 0; i < list.Len(); i++ {
		m, ok := list.Index(i).Interface().(map[string]interface{})
		if !ok || m["name"] == "" {
			t.Fatalf("条目 %d 规整结果异常: %#v", i, list.Index(i).Interface())
		}
	}
}
