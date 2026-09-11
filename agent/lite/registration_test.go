package lite

import (
	"testing"

	"github.com/rulego/rulego"
)

// lite 测试二进制不引入 eino agent 包:ai/agent 应解析到本实现,
// 旧名 ai/agentLite 不再注册(同名化后废弃)。
func TestSameNameRegistration(t *testing.T) {
	n, err := rulego.Registry.NewNode(NodeType)
	if err != nil {
		t.Fatalf("NewNode(%s): %v", NodeType, err)
	}
	if _, ok := n.(*AgentLiteNode); !ok {
		t.Fatalf("%s 应为 lite 实现,得到 %T", NodeType, n)
	}
	if n.Type() != "ai/agent" {
		t.Fatalf("节点类型应为 ai/agent: %q", n.Type())
	}
	if _, err := rulego.Registry.NewNode("ai/agentLite"); err == nil {
		t.Fatal("ai/agentLite 已废弃,不应可解析")
	}
}
