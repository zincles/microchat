// Package judge：JEV（SystemOne）决策模型的**裸调用**。
//
// 它只做一件事：把 `state + questions` 按 systemone 协议发出去，解析 `answers`。
// **没有能力开关、没有会话绑定、没有 Task 挂号、不碰库** —— 这些属于调用方；
// 传统 Agent 上的 `judge` 能力已经彻底删除，JEV 现在只作为 Flow 的判断节点使用。
package judge

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"microchat/internal/providers"
)

// judgeTimeout：这一发的总超时（ctx 可更早取消）。
const judgeTimeout = 10 * time.Second

// Answers：JEV 回的概率表（问题 id → 选中项/概率，由调用方解释）。
type Answers map[string]any

// Evaluate：拼请求、发出去、解析 `{answers, usage?, model?}`（只取 `answers`）。
//
// 只认 2xx；`answers` 必须是 object，否则报错。**失败原样返回，不重试**
// （重试由调用方决定 —— 与 `title` / `compact` 同一条脾气）。
func Evaluate(ctx context.Context, provider providers.Provider, model string, state any, questions map[string]any) (Answers, error) {
	request, err := providers.BuildSystemOne(provider, model, state, questions)
	if err != nil {
		return nil, err
	}
	request = request.WithContext(ctx)

	client := &http.Client{Timeout: judgeTimeout}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()

	if response.StatusCode < 200 || response.StatusCode > 299 {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 4096))
		return nil, fmt.Errorf("judge: 上游回 %d：%s", response.StatusCode, string(body))
	}

	var parsed struct {
		Answers json.RawMessage `json:"answers"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 64*1024)).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("judge: 响应不是 JSON：%w", err)
	}
	if len(parsed.Answers) == 0 {
		return nil, fmt.Errorf("judge: 响应里没有 answers")
	}
	var answers Answers
	if err := json.Unmarshal(parsed.Answers, &answers); err != nil {
		return nil, fmt.Errorf("judge: answers 不是 object：%w", err)
	}
	return answers, nil
}
