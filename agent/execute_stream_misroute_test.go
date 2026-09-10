package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/cloudwego/eino/schema"
	"github.com/rulego/rulego-components-ai/aspect"
	"github.com/rulego/rulego-components-ai/config"
	"github.com/rulego/rulego/api/types"
	"github.com/stretchr/testify/assert"
)

// misrouteStream 先文本后工具调用的流：window 模式下会误判为纯文本的特征
func misrouteStream() *schema.StreamReader[*schema.Message] {
	reader, writer := schema.Pipe[*schema.Message](1)
	go func() {
		defer writer.Close()
		writer.Send(&schema.Message{Content: "正在执行", Role: schema.Assistant}, nil)
		writer.Send(&schema.Message{ToolCalls: []schema.ToolCall{{ID: "c1", Type: "function", Function: schema.FunctionCall{Name: "bash", Arguments: "{}"}}}, Role: schema.Assistant}, nil)
	}()
	return reader
}

func textStream(content string) *schema.StreamReader[*schema.Message] {
	reader, writer := schema.Pipe[*schema.Message](1)
	go func() {
		defer writer.Close()
		writer.Send(&schema.Message{Content: content, Role: schema.Assistant}, nil)
	}()
	return reader
}

func misrouteTestExecutor(t *testing.T, streamCheck *streamCheckState) *AgentAspectExecutor {
	executor := NewAgentAspectExecutor(NewTestLogger(t))
	executor.manager = aspect.NewAspectManager()
	executor.streamCheck = streamCheck
	return executor
}

func misrouteTestOptions() (ExecuteOptions, *aspect.AgentInput, []*schema.Message) {
	opts := ExecuteOptions{
		ChainId:   "test_chain",
		AgentName: "test_agent",
		Msg:       types.NewMsg(0, "TEST", types.JSON, types.NewMetadata(), ""),
	}
	return opts, &aspect.AgentInput{}, []*schema.Message{{Role: schema.User, Content: "Hello"}}
}

// TestExecuteStream_MisroutedToolCalls_Retry END 路径流里出现工具调用＝被误判为纯文本：
// 升级为 drain 并对本轮输入重跑一次，重跑内容接在已流出内容之后
func TestExecuteStream_MisroutedToolCalls_Retry(t *testing.T) {
	state := newStreamCheckState(streamCheckWindow)
	executor := misrouteTestExecutor(t, state)

	opts, agentInput, messages := misrouteTestOptions()
	calls := 0
	streamExecutor := func(ctx context.Context, msgs []*schema.Message) (*schema.StreamReader[*schema.Message], error) {
		calls++
		if calls == 1 {
			return misrouteStream(), nil
		}
		return textStream("答案"), nil
	}

	var chunks []string
	output, err := executor.ExecuteStream(context.Background(), opts, agentInput, messages, streamExecutor, func(content, reasoning string, isFirst bool) {
		chunks = append(chunks, content)
	})

	assert.NoError(t, err)
	assert.Equal(t, 2, calls, "误判后应重跑一次")
	assert.Equal(t, StreamCheckDrain, state.load(), "误判后应升级为 drain")
	if assert.NotNil(t, output) {
		assert.Nil(t, output.Error)
		assert.Equal(t, "正在执行答案", output.Content)
	}
	assert.Equal(t, []string{"正在执行", "答案"}, chunks)
}

// TestExecuteStream_MisroutedToolCalls_AlreadyDrain 已是 drain 仍出现工具调用
// （流超过 MaxStreamChunks 护栏提前放弃判定的形态）无法自愈，不重跑
func TestExecuteStream_MisroutedToolCalls_AlreadyDrain(t *testing.T) {
	state := newStreamCheckState(StreamCheckDrain)
	executor := misrouteTestExecutor(t, state)

	opts, agentInput, messages := misrouteTestOptions()
	calls := 0
	streamExecutor := func(ctx context.Context, msgs []*schema.Message) (*schema.StreamReader[*schema.Message], error) {
		calls++
		return misrouteStream(), nil
	}

	output, err := executor.ExecuteStream(context.Background(), opts, agentInput, messages, streamExecutor, func(string, string, bool) {})

	assert.NoError(t, err)
	assert.Equal(t, 1, calls)
	assert.Equal(t, StreamCheckDrain, state.load())
	if assert.NotNil(t, output) {
		assert.Equal(t, "正在执行", output.Content)
	}
}

// TestExecuteStream_MisroutedToolCalls_NoCheckState 未挂判定模式持有者（无工具智能体）时不升级不重跑
func TestExecuteStream_MisroutedToolCalls_NoCheckState(t *testing.T) {
	executor := misrouteTestExecutor(t, nil)

	opts, agentInput, messages := misrouteTestOptions()
	calls := 0
	streamExecutor := func(ctx context.Context, msgs []*schema.Message) (*schema.StreamReader[*schema.Message], error) {
		calls++
		return misrouteStream(), nil
	}

	output, err := executor.ExecuteStream(context.Background(), opts, agentInput, messages, streamExecutor, func(string, string, bool) {})

	assert.NoError(t, err)
	assert.Equal(t, 1, calls)
	if assert.NotNil(t, output) {
		assert.Equal(t, "正在执行", output.Content)
	}
}

// TestExecuteStream_MisroutedToolCalls_StreamError 流中途出错时不升级不重跑（误判特征要求流无中途错误）
func TestExecuteStream_MisroutedToolCalls_StreamError(t *testing.T) {
	state := newStreamCheckState(streamCheckWindow)
	executor := misrouteTestExecutor(t, state)

	opts, agentInput, messages := misrouteTestOptions()
	calls := 0
	streamExecutor := func(ctx context.Context, msgs []*schema.Message) (*schema.StreamReader[*schema.Message], error) {
		calls++
		reader, writer := schema.Pipe[*schema.Message](1)
		go func() {
			writer.Send(&schema.Message{Content: "正在执行", Role: schema.Assistant}, nil)
			writer.Send(&schema.Message{ToolCalls: []schema.ToolCall{{ID: "c1", Type: "function", Function: schema.FunctionCall{Name: "bash", Arguments: "{}"}}}, Role: schema.Assistant}, nil)
			writer.Send(nil, errors.New("Error in input stream"))
			writer.Close()
		}()
		return reader, nil
	}

	output, err := executor.ExecuteStream(context.Background(), opts, agentInput, messages, streamExecutor, func(string, string, bool) {})

	assert.Error(t, err)
	assert.Equal(t, 1, calls)
	assert.Equal(t, streamCheckWindow, state.load(), "出错路径不应升级")
	if assert.NotNil(t, output) {
		assert.Error(t, output.Error)
	}
}

// TestExecuteStream_MisroutedToolCalls_ConcurrentEscalate 并发轮在本轮模型调用期间升级时，
// startMode 仍按发起时的 window 判定，本轮误判照常重跑
func TestExecuteStream_MisroutedToolCalls_ConcurrentEscalate(t *testing.T) {
	state := newStreamCheckState(streamCheckWindow)
	executor := misrouteTestExecutor(t, state)

	opts, agentInput, messages := misrouteTestOptions()
	calls := 0
	streamExecutor := func(ctx context.Context, msgs []*schema.Message) (*schema.StreamReader[*schema.Message], error) {
		calls++
		// 模拟并发运行在本轮模型调用期间升级
		state.escalate()
		if calls == 1 {
			return misrouteStream(), nil
		}
		return textStream("答案"), nil
	}

	output, err := executor.ExecuteStream(context.Background(), opts, agentInput, messages, streamExecutor, func(string, string, bool) {})

	assert.NoError(t, err)
	assert.Equal(t, 2, calls, "并发升级不能吞掉本轮重跑")
	assert.Equal(t, StreamCheckDrain, state.load())
	if assert.NotNil(t, output) {
		assert.Nil(t, output.Error)
		assert.Equal(t, "正在执行答案", output.Content)
	}
}

// TestExecuteStream_MisroutedToolCalls_GuardTripped 本轮流被 MaxStreamChunks 护栏截断时
// 不重跑（重跑同样不完整），升级仍生效保后续轮次
func TestExecuteStream_MisroutedToolCalls_GuardTripped(t *testing.T) {
	state := newStreamCheckState(streamCheckWindow)
	executor := misrouteTestExecutor(t, state)

	opts, agentInput, messages := misrouteTestOptions()
	calls := 0
	streamExecutor := func(ctx context.Context, msgs []*schema.Message) (*schema.StreamReader[*schema.Message], error) {
		calls++
		reader, writer := schema.Pipe[*schema.Message](1)
		go func() {
			defer writer.Close()
			writer.Send(&schema.Message{Content: "正在执行", Role: schema.Assistant}, nil)
			writer.Send(&schema.Message{ToolCalls: []schema.ToolCall{{ID: "c1", Type: "function", Function: schema.FunctionCall{Name: "bash", Arguments: "{}"}}}, Role: schema.Assistant}, nil)
			for i := 0; i < config.MaxStreamChunks; i++ {
				writer.Send(&schema.Message{Content: "后续", Role: schema.Assistant}, nil)
			}
		}()
		return reader, nil
	}

	output, err := executor.ExecuteStream(context.Background(), opts, agentInput, messages, streamExecutor, func(string, string, bool) {})

	assert.NoError(t, err)
	assert.Equal(t, 1, calls, "护栏截断后不应重跑")
	assert.Equal(t, StreamCheckDrain, state.load(), "升级仍应生效")
	if assert.NotNil(t, output) {
		assert.Nil(t, output.Error)
		assert.Contains(t, output.Content, "正在执行")
	}
}
