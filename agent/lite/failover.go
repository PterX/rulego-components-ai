// failover.go 模型端点容灾路由。
//
// 主端点重试耗尽后按序切换备用端点(failover 配置);每个端点配独立熔断器,
// 连续失败进入指数冷却(30s 起倍增,封顶 10 分钟),冷却期内跳过,成功即复位。
// 全部端点冷却时快速失败,不继续冲击上游。
//
// 流式容灾只覆盖建立阶段:已有增量交付下游后中断不切换端点——重放会把
// 已输出内容重复发给下游,与 Client.Stream 的重试边界一致。
package lite

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// FailoverEndpoint 备用模型端点配置。
type FailoverEndpoint struct {
	Url   string `json:"url"`
	Key   string `json:"key"`
	Model string `json:"model,omitempty"` // 空=沿用主端点 model
}

// 熔断冷却参数:基础冷却可配(circuitCooldownSec,0=默认 60s,与 eino 版一致),
// 持续失败逐次翻倍,封顶 10 分钟。
const (
	defaultBreakerCooldown = 60 * time.Second
	breakerMaxCooldown     = 10 * time.Minute
)

// breakerCooldown 配置秒数转基础冷却;非正值用默认。
func breakerCooldown(sec int) time.Duration {
	if sec <= 0 {
		return defaultBreakerCooldown
	}
	return time.Duration(sec) * time.Second
}

// chatEndpoint 一次请求里的可用端点;model 为该端点自有模型名,空=沿用请求 model。
type chatEndpoint struct {
	client *Client
	model  string
}

// endpointBreaker 连续失败计数与冷却截止时间。
type endpointBreaker struct {
	fails         int
	cooldownUntil time.Time
}

// chatRouter 多端点容灾路由。单端点配置等价直连,不参与熔断
// (没有可切换的对象,熔断只会白白拒绝服务)。
type chatRouter struct {
	mu       sync.Mutex
	breakers map[string]*endpointBreaker
	base     time.Duration // 基础冷却时长
	now      func() time.Time
}

func newChatRouter(base time.Duration) *chatRouter {
	return &chatRouter{breakers: map[string]*endpointBreaker{}, base: base, now: time.Now}
}

// setNow 注入时间源(测试用)。
func (r *chatRouter) setNow(now func() time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.now = now
}

// breakerLocked 取或建端点熔断器;调用方须已持 r.mu。
func (r *chatRouter) breakerLocked(url string) *endpointBreaker {
	b := r.breakers[url]
	if b == nil {
		b = &endpointBreaker{}
		r.breakers[url] = b
	}
	return b
}

func (r *chatRouter) open(url string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	b := r.breakers[url]
	return b != nil && r.now().Before(b.cooldownUntil)
}

// failure 记一次失败,冷却时长自基础值起随连续失败次数指数增长。
func (r *chatRouter) failure(url string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	b := r.breakerLocked(url)
	b.fails++
	cooldown := r.base
	for i := 1; i < b.fails && cooldown < breakerMaxCooldown; i++ {
		cooldown *= 2
	}
	if cooldown > breakerMaxCooldown {
		cooldown = breakerMaxCooldown
	}
	b.cooldownUntil = r.now().Add(cooldown)
}

// success 复位熔断状态。
func (r *chatRouter) success(url string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	b := r.breakerLocked(url)
	b.fails = 0
	b.cooldownUntil = time.Time{}
}

// applyModel 端点 model 非空时覆盖请求 model。
func applyModel(req Request, ep chatEndpoint) Request {
	if ep.model != "" {
		req.Model = ep.model
	}
	return req
}

// allOpenError 全部端点冷却时的错误,附最近恢复时间。
func (r *chatRouter) allOpenError() error {
	r.mu.Lock()
	now := r.now()
	soonest := time.Time{}
	for _, b := range r.breakers {
		if now.Before(b.cooldownUntil) && (soonest.IsZero() || b.cooldownUntil.Before(soonest)) {
			soonest = b.cooldownUntil
		}
	}
	r.mu.Unlock()
	if soonest.IsZero() {
		return errors.New("无可用模型端点")
	}
	return fmt.Errorf("全部模型端点均处于熔断冷却,约 %s 后恢复重试", soonest.Sub(now).Round(time.Second))
}

// Complete 非流式请求:按端点顺序尝试,失败(该端点重试耗尽)切换下一端点。
func (r *chatRouter) Complete(ctx context.Context, eps []chatEndpoint, req Request) (*Response, error) {
	if len(eps) == 1 {
		return eps[0].client.Complete(ctx, applyModel(req, eps[0]))
	}
	var lastErr error
	for _, ep := range eps {
		if r.open(ep.client.baseURL) {
			continue
		}
		resp, err := ep.client.Complete(ctx, applyModel(req, ep))
		if err == nil {
			r.success(ep.client.baseURL)
			return resp, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			return nil, err // 调用方已取消,切换端点没有意义
		}
		r.failure(ep.client.baseURL)
	}
	if lastErr == nil {
		return nil, r.allOpenError()
	}
	return nil, fmt.Errorf("主端点与全部备用端点均失败,最后错误: %w", lastErr)
}

// Stream 流式请求:建立阶段失败(尚无增量交付)切换下一端点;已有增量
// 交付后中断直接返回错误,不切换不重放。
func (r *chatRouter) Stream(ctx context.Context, eps []chatEndpoint, req Request, onDelta StreamHandler) (string, Usage, error) {
	if len(eps) == 1 {
		return eps[0].client.Stream(ctx, applyModel(req, eps[0]), onDelta)
	}
	var lastErr error
	for _, ep := range eps {
		if r.open(ep.client.baseURL) {
			continue
		}
		delivered := false
		finish, usage, err := ep.client.Stream(ctx, applyModel(req, ep), func(d StreamDelta) error {
			delivered = true
			return onDelta(d)
		})
		if err == nil {
			r.success(ep.client.baseURL)
			return finish, usage, nil
		}
		lastErr = err
		if delivered || ctx.Err() != nil {
			return "", usage, err
		}
		r.failure(ep.client.baseURL)
	}
	if lastErr == nil {
		return "", Usage{}, r.allOpenError()
	}
	return "", Usage{}, fmt.Errorf("主端点与全部备用端点均失败,最后错误: %w", lastErr)
}
