// litemcp.go 注册表工具 → rulego 中立工具接口的桥接。
//
// 方向合法:tool/(eino 系)依赖 lite 之外的消费方,本桥只做接口适配。
package tool

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/cloudwego/eino/components/tool"
	"github.com/rulego/rulego/api/types"
)

// AsMCPToolProvider 把注册表内的工具桥接为 rulego 中立工具接口,
// 供 ai/agentLite 等只认 MCPToolProvider 的节点使用。
// 注入方式:rc.Udf[types.MCPToolProviderKey] = Registry.AsMCPToolProvider()。
func (r *ToolRegistry) AsMCPToolProvider() types.MCPToolProvider {
	return &registryMCPProvider{r: r}
}

type registryMCPProvider struct {
	r *ToolRegistry
}

// ListToolDefinitions 走 ToolRegistry.List() 的 ToolInfo→参数提取:
// ParamsOneOf.ToJSONSchema 把 params/jsonschema 两种定义统一成 JSON Schema。
func (p *registryMCPProvider) ListToolDefinitions() ([]types.MCPToolDefinition, error) {
	infos := p.r.List()
	out := make([]types.MCPToolDefinition, 0, len(infos))
	for _, info := range infos {
		def := types.MCPToolDefinition{Name: info.Name, Description: info.Desc}
		if info.ParamsOneOf != nil {
			if js, err := info.ParamsOneOf.ToJSONSchema(); err == nil && js != nil {
				if b, err := json.Marshal(js); err == nil {
					def.InputSchema = b
				}
			}
		}
		if len(def.InputSchema) == 0 {
			// 无参工具也给出合法 schema,消费方可直接内联为 tools 定义。
			def.InputSchema = []byte(`{"type":"object","properties":{}}`)
		}
		out = append(out, def)
	}
	return out, nil
}

// CallTool 参数 map 序列化后走 InvokableRun;注册表存的是 BaseTool,
// 不可调用型(如流式工具)在此报错。
func (p *registryMCPProvider) CallTool(ctx context.Context, name string, args map[string]interface{}) (string, error) {
	t, ok := p.r.Get(name)
	if !ok {
		return "", fmt.Errorf("工具 %s 未注册", name)
	}
	invokable, ok := t.(tool.InvokableTool)
	if !ok {
		return "", fmt.Errorf("工具 %s 不支持同步调用", name)
	}
	b, err := json.Marshal(args)
	if err != nil {
		return "", err
	}
	return invokable.InvokableRun(ctx, string(b))
}
