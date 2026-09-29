# TODO.md — 下一步做什么

只列**还没动**的东西 ✓；设计口径全在 `DEFINE.md` ✓，过程与来回不记（git 里有历史）。

## 现在就挡路的 ✓

1. **压缩**（`summarize` 能力 + `POST /sessions/{session_id}/compact`）——
   长战役不炸上下文就靠它。形状见 `DEFINE.md`（「能力」与「任务（Task）与两族调用」两节），
   落库前必须过那五条验证清单（剔 `<state>` / 非空 / 区间自洽 / 不许覆盖已压缩区间 / 写 `prompt_version` 与 `usage`）。
2. **能力开关**（`agents.json` 里每个 agent 上的 `abilities`）—— 关掉"总结"就该**做不了压缩**，
   而不是偷偷降级 ✓。只列真会做的两个：`summarize` / `title`；**不要** `abilities.json`（口子在 agent 上）。
3. **CORS + OPTIONS 预检** —— 服务端**现在没有**这两样 ✗（文档里那句"CORS 全开"是待做，不是现状）；
   没有它 Web 版一个请求都发不出去（带 JSON 的 POST 也会触发预检）。
4. **取尾 N 条** —— `GET /sessions/{session_id}/messages?limit=&before=`；
   现在一次全拉，几千条时前端会疼（"最后几个"是渲染的常态需求）。

## 接着做 ✓

5. **起标题**（`title` 能力）—— 现在只是拿首句截断，没有真的调模型。
6. **重摇（re-roll）+ 接受** —— 摇出来的正文先看；**接受后**就地替换正文（`message_id` 不变 ⇒
   走的还是"改正文"那条路，不需要新机制）。拒绝 ⇒ 丢掉（库里自始至终没有它）。
7. **思考流的呈现** —— 游标（`think_from`）已经在跑 ✓，界面还没画；口径：默认折叠成「思考（2.1s）」。
8. **`messages.updated_at`** —— "修改时"没存；顺带让 summary 的 `dirty` 判断变便宜。
9. **`prompt_version`** —— 列已经在了，还没人算（第一个能力落地时自然填上）。

## 收拾性质 ✓

10. 删死字段 `Agent.params` / `Agent.prompt_order`（声明了、没人读、用途未定）。
11. `TERM=dumb` 时满线退回 ASCII（现在的降级只管颜色与窄屏）。
12. `-debug` 补两个 op：`abilities` / `compact`（那两个落地后再加，别提前占位）。
13. `Task` 上加 `Ability` 字段（哪次作业发起了哪次能力调用）—— 见 `DEFINE.md` 的署名一条。

## 大件（想清楚再动）✓

14. **归档 / 剪枝 / 清原文** —— `AGENTS.md` 的「摘要 / 压缩的设计」里有两条铁律：
    剪枝前**必须先导出**、剪枝与落摘要**同一事务**。
15. **JEV 决策模型**（第三种 provider kind；只动尾部 / 失败降级 / 产出不进历史与变量）—— 见 `reminder/jev.md`。
16. **Godot 前端接真实数据**（`frontend/` 由用户在编辑器里自己设计）。
