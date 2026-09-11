package tool

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"github.com/eino-contrib/jsonschema"
	orderedmap "github.com/wk8/go-ordered-map/v2"
)

// echoBridgeTool 桥接测试用假工具:Info 带 JSON Schema 参数,InvokableRun 转大写。
type echoBridgeTool struct{}

func (echoBridgeTool) Info(context.Context) (*schema.ToolInfo, error) {
	props := orderedmap.New[string, *jsonschema.Schema]()
	props.Set("text", &jsonschema.Schema{Type: "string", Description: "输入文本"})
	return &schema.ToolInfo{
		Name: "echo_bridge",
		Desc: "参数转大写回显",
		ParamsOneOf: schema.NewParamsOneOfByJSONSchema(&jsonschema.Schema{
			Type:       "object",
			Properties: props,
			Required:   []string{"text"},
		}),
	}, nil
}

func (echoBridgeTool) InvokableRun(_ context.Context, args string, _ ...tool.Option) (string, error) {
	return strings.ToUpper(args), nil
}

// noParamTool 无参工具(ParamsOneOf 为 nil):桥应给出合法空 schema。
type noParamTool struct{}

func (noParamTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: "no_param", Desc: "无参工具"}, nil
}

func (noParamTool) InvokableRun(_ context.Context, _ string, _ ...tool.Option) (string, error) {
	return "pong", nil
}

func newTestRegistry() *ToolRegistry {
	return &ToolRegistry{
		tools: map[string]tool.BaseTool{},
		defs:  map[string]ToolDefinition{},
	}
}

func TestAsMCPToolProvider(t *testing.T) {
	reg := newTestRegistry()
	if err := reg.Register(echoBridgeTool{}); err != nil {
		t.Fatal(err)
	}
	if err := reg.Register(noParamTool{}); err != nil {
		t.Fatal(err)
	}

	p := reg.AsMCPToolProvider()
	defs, err := p.ListToolDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	if len(defs) != 2 {
		t.Fatalf("应桥接出 2 个工具, got %d", len(defs))
	}
	foundEcho := false
	for _, d := range defs {
		if d.Name != "echo_bridge" {
			continue
		}
		foundEcho = true
		if d.Description != "参数转大写回显" {
			t.Errorf("描述未透传: %q", d.Description)
		}
		var probe struct {
			Type       string                     `json:"type"`
			Required   []string                   `json:"required"`
			Properties map[string]json.RawMessage `json:"properties"`
		}
		if err := json.Unmarshal(d.InputSchema, &probe); err != nil {
			t.Fatalf("InputSchema 应为合法 JSON: %v", err)
		}
		if probe.Properties["text"] == nil || len(probe.Required) == 0 {
			t.Errorf("InputSchema 未携带参数定义: %s", d.InputSchema)
		}
	}
	if !foundEcho {
		t.Fatal("缺少 echo_bridge 定义")
	}

	// 调用:参数 map 序列化后走 InvokableRun。
	out, err := p.CallTool(context.Background(), "echo_bridge", map[string]interface{}{"text": "abc"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, `ABC`) {
		t.Errorf("调用结果异常: %s", out)
	}

	// 未注册工具报错。
	if _, err := p.CallTool(context.Background(), "missing", nil); err == nil || !strings.Contains(err.Error(), "未注册") {
		t.Errorf("未注册工具应报错: %v", err)
	}
}

func TestAsMCPToolProviderNoParamSchema(t *testing.T) {
	reg := newTestRegistry()
	if err := reg.Register(noParamTool{}); err != nil {
		t.Fatal(err)
	}
	defs, err := reg.AsMCPToolProvider().ListToolDefinitions()
	if err != nil {
		t.Fatal(err)
	}
	if len(defs) != 1 || len(defs[0].InputSchema) == 0 {
		t.Fatalf("无参工具应有合法空 schema: %+v", defs)
	}
	var probe map[string]any
	if err := json.Unmarshal(defs[0].InputSchema, &probe); err != nil {
		t.Errorf("空 schema 不是合法 JSON: %v", err)
	}
	if probe["type"] != "object" {
		t.Errorf("空 schema 类型异常: %s", defs[0].InputSchema)
	}
}
