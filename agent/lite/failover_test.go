package lite

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// jsonLLM 记录请求体并返回固定非流式响应,models 记录每个请求的 model 字段。
type jsonLLM struct {
	mu       sync.Mutex
	requests []string
	models   []string
}

func (j *jsonLLM) handler(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var probe struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(body, &probe)
	j.mu.Lock()
	j.requests = append(j.requests, string(body))
	j.models = append(j.models, probe.Model)
	j.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"来自备用"},"finish_reason":"stop"}],` +
		`"usage":{"prompt_tokens":3,"completion_tokens":4,"total_tokens":7}}`))
}

func (j *jsonLLM) count() int {
	j.mu.Lock()
	defer j.mu.Unlock()
	return len(j.requests)
}

func (j *jsonLLM) lastModel() string {
	j.mu.Lock()
	defer j.mu.Unlock()
	if len(j.models) == 0 {
		return ""
	}
	return j.models[len(j.models)-1]
}

// notFoundLLM 恒 404(确定性失败,不走重试退避,测试快)。
type notFoundLLM struct{ hits int32 }

func (s *notFoundLLM) handler(w http.ResponseWriter, _ *http.Request) {
	atomic.AddInt32(&s.hits, 1)
	w.WriteHeader(http.StatusNotFound)
	_, _ = w.Write([]byte(`{"error":{"message":"no such model"}}`))
}

func (s *notFoundLLM) hitCount() int { return int(atomic.LoadInt32(&s.hits)) }

// 请求体采样参数的线上字段名遵循 OpenAI 协议(top_p/max_completion_tokens,
// 与 eino 版 wire 一致),不是节点配置的 topP/maxTokens。
func TestParamsWireFieldNames(t *testing.T) {
	b, err := json.Marshal(Request{Model: "m", Params: Params{Temperature: 0.5, TopP: 0.9, MaxTokens: 100}})
	if err != nil {
		t.Fatal(err)
	}
	body := string(b)
	for _, want := range []string{`"temperature":0.5`, `"top_p":0.9`, `"max_completion_tokens":100`} {
		if !strings.Contains(body, want) {
			t.Errorf("请求体缺 %s: %s", want, body)
		}
	}
	if strings.Contains(body, "topP") || strings.Contains(body, `"max_tokens"`) {
		t.Errorf("请求体不应出现配置字段名: %s", body)
	}
}

// 主端点确定性失败(404 不重试)后切换备用端点。
func TestChatRouterFailoverComplete(t *testing.T) {
	primary := &notFoundLLM{}
	pSrv := httptest.NewServer(http.HandlerFunc(primary.handler))
	defer pSrv.Close()
	backup := &jsonLLM{}
	bSrv := httptest.NewServer(http.HandlerFunc(backup.handler))
	defer bSrv.Close()

	r := newChatRouter(breakerCooldown(0))
	resp, err := r.Complete(context.Background(), []chatEndpoint{
		{client: New(pSrv.URL, "")}, {client: New(bSrv.URL, "")},
	}, Request{Model: "m", Messages: []Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Choices[0].Message.Content != "来自备用" {
		t.Errorf("应取到备用端点回答: %+v", resp.Choices[0].Message)
	}
	if backup.count() != 1 || primary.hitCount() != 1 {
		t.Errorf("主/备用命中异常: primary=%d backup=%d", primary.hitCount(), backup.count())
	}
}

// 冷却期内跳过已熔断端点;冷却到期后重新尝试。
func TestChatRouterBreakerCooldown(t *testing.T) {
	primary := &notFoundLLM{}
	pSrv := httptest.NewServer(http.HandlerFunc(primary.handler))
	defer pSrv.Close()
	backup := &jsonLLM{}
	bSrv := httptest.NewServer(http.HandlerFunc(backup.handler))
	defer bSrv.Close()

	cur := time.Now()
	r := newChatRouter(breakerCooldown(0))
	r.setNow(func() time.Time { return cur })
	eps := []chatEndpoint{{client: New(pSrv.URL, "")}, {client: New(bSrv.URL, "")}}
	req := Request{Model: "m", Messages: []Message{{Role: "user", Content: "hi"}}}

	if _, err := r.Complete(context.Background(), eps, req); err != nil {
		t.Fatal(err)
	}
	primaryHits := primary.hitCount()

	// 默认冷却 60s 未过:主端点不再被请求,直接走备用。
	cur = cur.Add(10 * time.Second)
	if _, err := r.Complete(context.Background(), eps, req); err != nil {
		t.Fatal(err)
	}
	if got := primary.hitCount(); got != primaryHits {
		t.Errorf("冷却期内不应再打主端点: hits %d → %d", primaryHits, got)
	}

	// 冷却到期:主端点重新尝试(仍 404,再次熔断)。
	cur = cur.Add(51 * time.Second)
	if _, err := r.Complete(context.Background(), eps, req); err != nil {
		t.Fatal(err)
	}
	if got := primary.hitCount(); got != primaryHits+1 {
		t.Errorf("冷却到期后应重试主端点: hits %d → %d", primaryHits, got)
	}
}

// 全部端点熔断时快速失败,错误可读;冷却到期后恢复尝试。
func TestChatRouterAllOpenError(t *testing.T) {
	pri, bak := &notFoundLLM{}, &notFoundLLM{}
	pSrv := httptest.NewServer(http.HandlerFunc(pri.handler))
	defer pSrv.Close()
	bSrv := httptest.NewServer(http.HandlerFunc(bak.handler))
	defer bSrv.Close()

	cur := time.Now()
	r := newChatRouter(breakerCooldown(0))
	r.setNow(func() time.Time { return cur })
	eps := []chatEndpoint{{client: New(pSrv.URL, "")}, {client: New(bSrv.URL, "")}}
	req := Request{Model: "m", Messages: []Message{{Role: "user", Content: "hi"}}}

	if _, err := r.Complete(context.Background(), eps, req); err == nil {
		t.Fatal("双端点 404 应失败")
	}
	cur = cur.Add(time.Second)
	_, err := r.Complete(context.Background(), eps, req)
	if err == nil || !strings.Contains(err.Error(), "熔断冷却") {
		t.Fatalf("全部端点冷却应快速失败并说明原因: %v", err)
	}
	if pri.hitCount() != 1 || bak.hitCount() != 1 {
		t.Errorf("冷却期不应再请求端点: pri=%d bak=%d", pri.hitCount(), bak.hitCount())
	}

	cur = cur.Add(60 * time.Second)
	if _, err := r.Complete(context.Background(), eps, req); err == nil {
		t.Fatal("到期后应恢复尝试(端点仍 404,返回失败而非熔断错误)")
	}
	if pri.hitCount() != 2 {
		t.Errorf("到期后主端点应被重试: %d", pri.hitCount())
	}
}

// 流已交付增量后中断:不切换端点(重放会重复输出),直接报错。
func TestChatRouterStreamDeliveredNoFailover(t *testing.T) {
	var hits int32
	pSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt32(&hits, 1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprintf(w, "data: %s\n\n", contentFrame("部分输出"))
		w.(http.Flusher).Flush()
		panic(http.ErrAbortHandler) // 中断连接,模拟上游半路断流
	}))
	defer pSrv.Close()
	backup := &jsonLLM{}
	bSrv := httptest.NewServer(http.HandlerFunc(backup.handler))
	defer bSrv.Close()

	r := newChatRouter(breakerCooldown(0))
	var deltas []string
	_, _, err := r.Stream(context.Background(), []chatEndpoint{
		{client: New(pSrv.URL, "")}, {client: New(bSrv.URL, "")},
	}, Request{Model: "m"}, func(d StreamDelta) error {
		deltas = append(deltas, d.Content)
		return nil
	})
	if err == nil {
		t.Fatal("断流应返回错误")
	}
	if strings.Join(deltas, "") != "部分输出" {
		t.Errorf("已交付增量应保留: %v", deltas)
	}
	if backup.count() != 0 {
		t.Errorf("增量已交付后不应切换备用端点: %d", backup.count())
	}
}

// 流建立阶段失败(无增量交付):切换备用端点续流。
func TestChatRouterStreamFailover(t *testing.T) {
	primary := &notFoundLLM{}
	pSrv := httptest.NewServer(http.HandlerFunc(primary.handler))
	defer pSrv.Close()
	backup := &sseLLM{}
	backup.scripts = [][]string{{contentFrame("备用回答"), doneFrame("stop")}}
	bSrv := httptest.NewServer(http.HandlerFunc(backup.handler))
	defer bSrv.Close()

	r := newChatRouter(breakerCooldown(0))
	var deltas []string
	finish, _, err := r.Stream(context.Background(), []chatEndpoint{
		{client: New(pSrv.URL, "")}, {client: New(bSrv.URL, "")},
	}, Request{Model: "m"}, func(d StreamDelta) error {
		deltas = append(deltas, d.Content)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if finish != "stop" || strings.Join(deltas, "") != "备用回答" {
		t.Errorf("备用端点流式结果异常: finish=%q deltas=%v", finish, deltas)
	}
}

// 备用端点 model 非空时覆盖请求 model(跨供应商模型名不同)。
func TestChatRouterModelOverride(t *testing.T) {
	primary := &notFoundLLM{}
	pSrv := httptest.NewServer(http.HandlerFunc(primary.handler))
	defer pSrv.Close()
	backup := &jsonLLM{}
	bSrv := httptest.NewServer(http.HandlerFunc(backup.handler))
	defer bSrv.Close()

	r := newChatRouter(breakerCooldown(0))
	req := Request{Model: "main-model", Messages: []Message{{Role: "user", Content: "hi"}}}
	if _, err := r.Complete(context.Background(), []chatEndpoint{
		{client: New(pSrv.URL, "")}, {client: New(bSrv.URL, ""), model: "backup-model"},
	}, req); err != nil {
		t.Fatal(err)
	}
	if backup.lastModel() != "backup-model" {
		t.Errorf("备用端点应收到的 model=backup-model, got %q", backup.lastModel())
	}
}

// 节点级:主端点 404,failover 配置生效,回答来自备用端点且使用其 model。
func TestAgentNodeFailover(t *testing.T) {
	primary := &notFoundLLM{}
	pSrv := httptest.NewServer(http.HandlerFunc(primary.handler))
	defer pSrv.Close()
	backup := &sseLLM{}
	backup.scripts = [][]string{{contentFrame("容灾成功"), doneFrame("stop")}}
	bSrv := httptest.NewServer(http.HandlerFunc(backup.handler))
	defer bSrv.Close()

	extra := fmt.Sprintf(`, "failover": [{"url": %q, "key": "bk", "model": "backup-model"}]`, bSrv.URL)
	eng := buildAgentChain(t, pSrv.URL, extra, false)
	t.Cleanup(func() { eng.Stop(context.Background()) })

	frames := runStreamMsg(t, eng, `{"messages":[{"role":"user","content":"hi"}]}`)
	var final string
	for _, f := range frames {
		if f.GetMetadata().GetValue("full_content") == "true" {
			final = f.GetData()
		}
	}
	if final != "容灾成功" {
		t.Errorf("应经备用端点得到回答: %q", final)
	}
	backup.mu.Lock()
	defer backup.mu.Unlock()
	if len(backup.requests) == 0 || !strings.Contains(backup.requests[0], `"model":"backup-model"`) {
		t.Errorf("备用端点应收到 backup-model 请求: %v", backup.requests)
	}
}
