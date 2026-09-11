package lite

// allowlist_test.go 允许列表的节点级流程验证(纯函数之外的接线):
//   - Tools 同时约束「暴露给模型的工具定义」与「实际执行」:模型点名列表外
//     工具必须被拒且不得触达 provider——per-chain 工具过滤是控制边界,不是提示;
//   - Skills 按智能体勾选生效:列表外技能不进 system prompt,skill 工具读不到。

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/rulego/rulego"
	"github.com/rulego/rulego/api/types"
)

// multiToolProvider 多工具假提供者,记录每次真实调用(副作用断言用)。
type multiToolProvider struct {
	mu      sync.Mutex
	invoked []string
}

func (p *multiToolProvider) ListToolDefinitions() ([]types.MCPToolDefinition, error) {
	return []types.MCPToolDefinition{
		{Name: "list_devices", Description: "列出设备", InputSchema: []byte(`{"type":"object","properties":{}}`)},
		{Name: "write_device_property", Description: "写设备属性", InputSchema: []byte(`{"type":"object","properties":{}}`)},
	}, nil
}

func (p *multiToolProvider) CallTool(_ context.Context, name string, _ map[string]interface{}) (string, error) {
	p.mu.Lock()
	p.invoked = append(p.invoked, name)
	p.mu.Unlock()
	if name == "list_devices" {
		return `{"devices":["meter-1"]}`, nil
	}
	return `{"written":true}`, nil
}

func (p *multiToolProvider) calls() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.invoked...)
}

// buildLiteChain 用完整 configuration 构建单节点链(独立于 buildAgentChain,任意配置可用)。
func buildLiteChain(t *testing.T, baseURL, cfg string, provider any) types.RuleEngine {
	t.Helper()
	def := fmt.Sprintf(`{
		"ruleChain": {"id": "t_lite_allowlist", "name": "t", "root": true},
		"metadata": {
			"nodes": [
				{"id": "n1", "type": %q, "name": "agent", "configuration": %s},
				{"id": "n2", "type": "end", "name": "end", "configuration": {}}
			],
			"connections": [
				{"fromId": "n1", "toId": "n2", "type": "Success"},
				{"fromId": "n1", "toId": "n2", "type": "Stream"}
			]
		}
	}`, NodeType, cfg)
	rc := rulego.NewConfig()
	if provider != nil {
		rc.Udf = map[string]any{types.MCPToolProviderKey: provider}
	}
	eng, err := rulego.NewRuleGo().New("t_lite_allowlist", []byte(def), types.WithConfig(rc))
	if err != nil {
		t.Fatalf("部署链: %v", err)
	}
	t.Cleanup(func() { eng.Stop(context.Background()) })
	return eng
}

// llmToolNames 抽取请求体里声明的工具名。
func llmToolNames(t *testing.T, raw string) []string {
	t.Helper()
	var req struct {
		Tools []Tool `json:"tools"`
	}
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		t.Fatalf("解析 LLM 请求体: %v", err)
	}
	names := make([]string, 0, len(req.Tools))
	for _, tl := range req.Tools {
		names = append(names, tl.Function.Name)
	}
	return names
}

// llmToolResult 拼接请求体里所有 role:tool 消息正文。
func llmToolResult(raw string) string {
	var req struct {
		Messages []Message `json:"messages"`
	}
	if json.Unmarshal([]byte(raw), &req) != nil {
		return ""
	}
	var b strings.Builder
	for _, m := range req.Messages {
		if m.Role == "tool" {
			b.WriteString(m.Content)
			b.WriteString("\n")
		}
	}
	return b.String()
}

// 模型点名允许列表外的写工具:必须被拒且 provider 未被执行;下一轮请求里
// role:tool 携带拒绝原因供模型改路。
func TestToolsAllowlistBlocksExecution(t *testing.T) {
	srv := &sseLLM{}
	httpSrv := httptest.NewServer(http.HandlerFunc(srv.handler))
	defer httpSrv.Close()
	srv.scripts = [][]string{
		// 首轮:模型无视工具表,直接点名列表外的写工具。
		{tcFrame(0, "c1", "write_device_property", `{"value":100}`), doneFrame("tool_calls")},
		{contentFrame("该工具未开放,无法执行"), doneFrame("stop")},
	}

	prov := &multiToolProvider{}
	cfg := `{"url":"` + httpSrv.URL + `","key":"k","model":"m","maxStep":5,"tools":["list_devices"]}`
	eng := buildLiteChain(t, httpSrv.URL, cfg, prov)

	frames := runStreamMsg(t, eng, `{"messages":[{"role":"user","content":"把阀门开到100"}]}`)
	var final string
	for _, f := range frames {
		if f.GetMetadata().GetValue("full_content") == "true" {
			final = f.GetData()
		}
	}
	if final == "" {
		t.Fatalf("链未正常收尾: %d 帧", len(frames))
	}

	// 暴露面:首轮请求只声明允许列表内的工具。
	srv.mu.Lock()
	first, second := srv.requests[0], ""
	if len(srv.requests) > 1 {
		second = srv.requests[1]
	}
	srv.mu.Unlock()
	if got := llmToolNames(t, first); len(got) != 1 || got[0] != "list_devices" {
		t.Fatalf("首轮应只暴露 list_devices,得到 %v", got)
	}

	// 控制边界:provider 不得收到列表外工具的调用。
	for _, c := range prov.calls() {
		if c == "write_device_property" {
			t.Fatalf("列表外写工具被执行了: %v", prov.calls())
		}
	}
	// 模型能看见拒绝原因(而非伪造的成功回执)。
	if tr := llmToolResult(second); !strings.Contains(tr, "write_device_property") || !strings.Contains(tr, "未开放") {
		t.Fatalf("role:tool 未携带拒绝语义: %q", tr)
	}
	if tr := llmToolResult(second); strings.Contains(tr, `{"written":true}`) {
		t.Fatalf("拒绝路径却回传了执行结果: %q", tr)
	}
}

// 技能按智能体勾选生效:列表外技能不进 system prompt、skill 工具读不到;
// 不勾选(空)时目录下全部启用技能可用。
func TestSkillsPerAgentSelection(t *testing.T) {
	dir := t.TempDir()
	write := func(dirName, front, body string) {
		d := filepath.Join(dir, dirName)
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(d, "SKILL.md"), []byte(front+"\n"+body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("a", "---\nname: 巡检\n---\n", "巡检流程全文")
	write("b", "---\nname: 保养\n---\n", "保养流程全文")
	write("c", "---\nname: 下线技能\nenabled: false\n---\n", "不应出现")

	srv := &sseLLM{}
	httpSrv := httptest.NewServer(http.HandlerFunc(srv.handler))
	defer httpSrv.Close()
	srv.scripts = [][]string{
		{tcFrame(0, "c1", "skill", `{"name":"保养"}`), doneFrame("tool_calls")},
		{contentFrame("该技能未启用"), doneFrame("stop")},
	}

	cfg := `{"url":"` + httpSrv.URL + `","key":"k","model":"m","maxStep":5,"skillsDir":"` + filepath.ToSlash(dir) + `","skills":["巡检"]}`
	eng := buildLiteChain(t, httpSrv.URL, cfg, nil)
	frames := runStreamMsg(t, eng, `{"messages":[{"role":"user","content":"设备该怎么保养"}]}`)
	var final string
	for _, f := range frames {
		if f.GetMetadata().GetValue("full_content") == "true" {
			final = f.GetData()
		}
	}
	if final == "" {
		t.Fatalf("链未正常收尾: %d 帧", len(frames))
	}

	srv.mu.Lock()
	first, second := srv.requests[0], srv.requests[1]
	srv.mu.Unlock()

	// system prompt 只出现勾选的技能。
	var req0 struct {
		Messages []Message `json:"messages"`
	}
	if err := json.Unmarshal([]byte(first), &req0); err != nil {
		t.Fatal(err)
	}
	sys := ""
	for _, m := range req0.Messages {
		if m.Role == "system" {
			sys = m.Content
		}
	}
	if !strings.Contains(sys, "巡检") {
		t.Fatalf("system prompt 缺勾选技能「巡检」: %q", sys)
	}
	if strings.Contains(sys, "保养") {
		t.Fatalf("未勾选的「保养」不应进 system prompt: %q", sys)
	}
	// skill 工具仍在(有已启用技能),但读不到未勾选技能的正文。
	if tr := llmToolResult(second); strings.Contains(tr, "保养流程全文") {
		t.Fatalf("未勾选技能被 skill 工具读到了: %q", tr)
	}

	// 不勾选=全部启用技能。
	srv2 := &sseLLM{}
	httpSrv2 := httptest.NewServer(http.HandlerFunc(srv2.handler))
	defer httpSrv2.Close()
	srv2.scripts = [][]string{{contentFrame("好的"), doneFrame("stop")}}
	cfg2 := `{"url":"` + httpSrv2.URL + `","key":"k","model":"m","maxStep":5,"skillsDir":"` + filepath.ToSlash(dir) + `"}`
	eng2 := buildLiteChain(t, httpSrv2.URL, cfg2, nil)
	runStreamMsg(t, eng2, `{"messages":[{"role":"user","content":"巡检和保养怎么做"}]}`)
	srv2.mu.Lock()
	raw := srv2.requests[0]
	srv2.mu.Unlock()
	if !strings.Contains(raw, "巡检") || !strings.Contains(raw, "保养") {
		t.Fatalf("空技能列表应载入全部启用技能: %s", raw)
	}
}
