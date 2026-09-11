package agent

import (
	"context"
	"testing"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"github.com/rulego/rulego"
	"github.com/rulego/rulego/api/types"
	"github.com/stretchr/testify/assert"

	"github.com/rulego/rulego-components-ai/config"
	aitool "github.com/rulego/rulego-components-ai/tool"
)

// shorthandTool 字符串速记解析测试的桩工具(注册进全局注册表,梯子第 3 级)。
type shorthandTool struct{}

func (shorthandTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: "stub_shorthand_tool", Desc: "shorthand stub"}, nil
}

func (shorthandTool) InvokableRun(_ context.Context, _ string, _ ...tool.Option) (string, error) {
	return "ok", nil
}

func init() {
	// 独特名字避免与其他测试的全局注册冲突。
	_ = aitool.Registry.Register(shorthandTool{})
}

// CreateTool 空 type(字符串速记展开结果)走与 builtin 相同的解析梯子。
func TestCreateToolNameShorthand(t *testing.T) {
	inst, info, _, err := CreateTool(config.Tool{Name: "stub_shorthand_tool"}, ToolOptions{})
	assert.Nil(t, err)
	assert.NotNil(t, inst)
	assert.Equal(t, "stub_shorthand_tool", info.Name)

	_, _, _, err = CreateTool(config.Tool{Name: "no_such_tool_anywhere"}, ToolOptions{})
	if err == nil || !assert.Contains(t, err.Error(), "tool not found") {
		t.Fatalf("未知工具名应报 tool not found: %v", err)
	}
}

// 节点 Init 全链路:链 DSL 里 tools 混排字符串与对象,字符串条目经
// NormalizeToolsShorthand 规整后随 mapstructure 解码,再走梯子装配成功。
func TestNodeInitToolsShorthand(t *testing.T) {
	node := &ReactAgentNode{}
	rc := rulego.NewConfig()
	err := node.Init(rc, types.Configuration{
		"url":   "http://127.0.0.1:1",
		"key":   "test-key",
		"model": "test-model",
		"tools": []interface{}{
			"stub_shorthand_tool",
			map[string]interface{}{"type": "rulechain", "name": "清理任务", "targetId": "tool_clean"},
		},
	})
	assert.Nil(t, err)
	assert.Equal(t, 2, len(node.Config.Tools))
	assert.Equal(t, "stub_shorthand_tool", node.Config.Tools[0].Name)
	assert.Equal(t, "", node.Config.Tools[0].Type)
	assert.Equal(t, config.ToolTypeRuleChain, node.Config.Tools[1].Type)
	node.Destroy()
}
