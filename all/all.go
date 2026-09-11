// Package all 一键引入所有 AI 组件。
// 通过空白导入触发各子包的 init() 自动注册。
//
// 一键引入:
//
//	import _ "github.com/rulego/rulego-components-ai/all"
//
// 按需引入，直接导入对应子包即可:
//
//	import _ "github.com/rulego/rulego-components-ai/agent"
//	import _ "github.com/rulego/rulego-components-ai/tool/bash"
package all

import (
	_ "github.com/rulego/rulego-components-ai/action"
	// 节点组件 - 注册到 rulego.Registry。agent(eino 版)必须在 agent/lite
	// 之前:两包共用类型名 ai/agent,同名先注册者生效,后到者注册失败自行让位。
	_ "github.com/rulego/rulego-components-ai/agent"
	_ "github.com/rulego/rulego-components-ai/agent/lite"
	_ "github.com/rulego/rulego-components-ai/intent"
	_ "github.com/rulego/rulego-components-ai/mcp"

	// 工具组件 - 注册到 tool.Registry
	_ "github.com/rulego/rulego-components-ai/tool/bash"
	_ "github.com/rulego/rulego-components-ai/tool/browseruse"
	_ "github.com/rulego/rulego-components-ai/tool/edit"
	_ "github.com/rulego/rulego-components-ai/tool/mcp"
	_ "github.com/rulego/rulego-components-ai/tool/read"
	_ "github.com/rulego/rulego-components-ai/tool/skill"
	_ "github.com/rulego/rulego-components-ai/tool/write"

	// Endpoint 组件
	_ "github.com/rulego/rulego-components-ai/endpoint"

	// Processor 组件
	_ "github.com/rulego/rulego-components-ai/processor"
)
