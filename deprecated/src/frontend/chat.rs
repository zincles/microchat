//! chat：从原来的 main.rs 拆出来的（纯搬家，零行为变更）。

use super::*;

impl App {
    pub(super) fn sidebar(&mut self, ui: &mut egui::Ui) {
        let mut switch_to: Option<Uuid> = None;
        let mut delete: Option<Uuid> = None;
        let mut new_conv = false;

        let mut open_debug = false;
        let mut refresh = false;
        egui::Panel::bottom("account_bar").show(ui, |ui| {
            ui.add_space(4.0);
            ui.separator();
            for (label, view) in [
                ("账户", View::Account),
                ("设置", View::Settings),
                ("任务", View::Tasks),
                ("调试", View::Debug),
            ] {
                if ui.selectable_label(self.view == view, label).clicked() {
                    self.view = view;
                    open_debug = view == View::Debug;
                }
            }
            ui.separator();
            // 后台在跑什么都不用翻页找：一行指示器，点一下就进「任务」页
            let running = self
                .task_board
                .as_ref()
                .map(|board| board.running)
                .unwrap_or(0);
            let label = if running == 0 {
                RichText::new("后台：空闲").small().weak()
            } else {
                RichText::new(format!("后台：跑着 {running} 个"))
                    .small()
                    .color(egui::Color32::from_rgb(120, 170, 120))
            };
            if ui
                .add(egui::Label::new(label).sense(egui::Sense::click()))
                .on_hover_text("点开看「任务」页：谁在跑、跑了多久、什么结果")
                .clicked()
            {
                self.view = View::Tasks;
            }
            ui.separator();
            if ui
                .button("⟳ 刷新")
                .on_hover_text("从服务器重拉一遍：会话、消息、变量、分支、模型、agent")
                .clicked()
            {
                refresh = true;
            }
            ui.add_space(4.0);
        });
        if open_debug {
            self.fetch_debug();
        }
        if refresh {
            self.refresh_all();
        }

        ui.add_space(8.0);
        if ui.button("＋ 新建对话").clicked() {
            new_conv = true;
        }
        ui.add_space(8.0);
        ui.separator();
        if self.conversations.is_empty() {
            ui.label(RichText::new("还没有会话：新建一个").weak());
        }
        egui::ScrollArea::vertical()
            .id_salt("conversation_list")
            .auto_shrink([false, false])
            .show(ui, |ui| {
                for conversation in &self.conversations {
                    let title = if conversation.title.is_empty() {
                        NEW_TITLE.to_owned()
                    } else {
                        conversation.title.clone()
                    };
                    // 正在生成的会话在列表里标一下：切走了也看得见它还活着
                    let title = if conversation.turn.phase.is_busy() {
                        format!("{title} · 生成中…")
                    } else {
                        title
                    };
                    ui.horizontal(|ui| {
                        // 会话标题必须**可截断**：不截断的话这一行的最小宽度等于标题全宽，
                        // 而 egui 不允许面板窄于内容最小宽度——分隔线就会"只能往右拖、往左拖不动"。
                        let room = 26.0; // 给右边的关闭按钮留位
                        let size = egui::vec2(
                            (ui.available_width() - room).max(40.0),
                            ui.spacing().interact_size.y,
                        );
                        // 先占位、铺底色，**再**把文字放进去——反过来的话底色会盖住标题。
                        let (rect, row) = ui.allocate_exact_size(size, egui::Sense::click());
                        if self.current == Some(conversation.id) {
                            ui.painter().rect_filled(
                                rect,
                                egui::CornerRadius::same(4),
                                ui.visuals().selection.bg_fill,
                            );
                        }
                        // `selectable(false)` 不能省：默认的 Label 会 sense 点击/拖动（为了选文本），
                        // 压在占位矩形上就把点击吃了——整行就点不动了。
                        ui.put(
                            rect.shrink2(egui::vec2(4.0, 0.0)),
                            egui::Label::new(&title).truncate().selectable(false),
                        );
                        if row.clicked() {
                            switch_to = Some(conversation.id);
                        }
                        row.on_hover_text(&title);
                        ui.with_layout(Layout::right_to_left(Align::Center), |ui| {
                            if close_button(ui).clicked() {
                                delete = Some(conversation.id);
                            }
                        });
                    });
                }
            });

        if new_conv {
            self.create_conversation();
        } else if let Some(id) = delete {
            self.delete_conversation(id);
        } else if let Some(id) = switch_to {
            self.open_conversation(id);
        }
    }
}

impl App {
    /// 右侧世界状态面板：生效值 + 本会话改动 + 全局层。
    ///
    /// 生效值由后端 fold 出来（前端不重算一遍——重算就有两个真相来源）。
    /// 带 ◆ 的是被本会话覆写过的键：一眼看出"这一局改了什么"。
    pub(super) fn vars_panel(&mut self, ui: &mut egui::Ui, conversation: Uuid) {
        let view = self
            .variables
            .as_ref()
            .filter(|_| self.variables_for == Some(conversation));
        let changed: Vec<&str> = view
            .map(|view| view.session.iter().map(|row| row.key.as_str()).collect())
            .unwrap_or_default();

        ui.add_space(4.0);
        ui.horizontal(|ui| {
            ui.label(RichText::new("世界状态").strong());
            if let Some(view) = view {
                ui.label(RichText::new(format!("本会话 {} 处改动", view.session.len())).weak());
            }
            ui.with_layout(Layout::right_to_left(Align::Center), |ui| {
                if ui
                    .small_button("复制协议")
                    .on_hover_text("把状态块语法复制到剪贴板，粘进 agent 的系统提示词")
                    .clicked()
                {
                    ui.output_mut(|out| {
                        out.commands.push(egui::OutputCommand::CopyText(
                            microchat::world::PROTOCOL_HINT.to_owned(),
                        ))
                    });
                }
            });
        });
        ui.separator();

        let Some(view) = view else {
            ui.label(RichText::new("世界状态还没拉到").weak());
            return;
        };

        egui::ScrollArea::vertical()
            .id_salt("vars_scroll")
            .auto_shrink([false, false])
            .show(ui, |ui| {
                ui.label(RichText::new("生效值（发给模型的就是这张表）").weak());
                if view.effective.is_empty() {
                    ui.label(RichText::new("（空）").weak());
                }
                for (key, value) in &view.effective {
                    let mark = if changed.contains(&key.as_str()) {
                        "◆ "
                    } else {
                        ""
                    };
                    ui.horizontal_wrapped(|ui| {
                        ui.label(RichText::new(format!("{mark}{key}")).monospace());
                        ui.label(RichText::new("=").weak());
                        ui.label(RichText::new(value).monospace());
                    });
                }

                ui.add_space(10.0);
                ui.label(RichText::new(format!("本会话改动（{}）", view.session.len())).weak());
                if view.session.is_empty() {
                    ui.label(RichText::new("（还没有）").weak());
                }
                for row in &view.session {
                    let text = match (&row.kind, &row.value) {
                        (microchat::world::OpKind::Set, Some(value)) => {
                            format!("{} = {value}", row.key)
                        }
                        (microchat::world::OpKind::Set, None) => format!("{} = （空串）", row.key),
                        (microchat::world::OpKind::Delete, _) => format!("delete({})", row.key),
                    };
                    ui.label(RichText::new(format!("#{} {}", row.seq, text)).monospace());
                }

                ui.add_space(10.0);
                ui.label(RichText::new(format!("全局层（{}）", view.global_values.len())).weak());
                for (key, value) in &view.global_values {
                    ui.label(RichText::new(format!("{key} = {value}")).monospace());
                }

                ui.add_space(10.0);
                ui.label(RichText::new("在消息末尾写：").weak());
                ui.label(
                    RichText::new("<state>\nHP = 12\ndel 火把\n</state>")
                        .monospace()
                        .weak(),
                );
            });
    }
}

impl App {
    /// 会话顶部那行：`上下文 12.3k（上轮实测 11.9k）/ 40k`。
    ///
    /// 数字来自 `/context`（**估算**）；括注是上一轮上游真报的 `prompt_tokens` ——
    /// 两者差多少，一眼就能看出估算漂没漂。超 80% 变黄、越过触发阈值变红。
    pub(super) fn context_row(&self, ui: &mut egui::Ui) {
        if self.context_usage_for != self.current {
            return;
        }
        let Some(usage) = self.context_usage.as_ref() else {
            return;
        };
        let short = |n: usize| short_tokens(n as i64);
        let ratio = usage.used_tokens as f64 / usage.budget_tokens.max(1) as f64;
        let color = if usage.used_tokens > usage.trigger_tokens {
            egui::Color32::from_rgb(220, 90, 90)
        } else if ratio >= 0.8 {
            egui::Color32::from_rgb(210, 170, 70)
        } else {
            ui.visuals().weak_text_color()
        };
        let measured = usage
            .last_prompt_tokens
            .map(|tokens| format!("（上轮实测 {}）", short(tokens as usize)))
            .unwrap_or_default();
        let mut line = format!(
            "上下文 {}{measured} / {}",
            short(usage.used_tokens),
            short(usage.budget_tokens)
        );
        if usage.over_budget {
            line.push_str(" · 已超出预算");
        } else if usage.used_tokens > usage.trigger_tokens {
            line.push_str(" · 已越过摘要触发阈值");
        }
        let hover = format!(
            "估算口径：字符数 ÷ {:.2}（models.tokenizer）\n上下文长度 {}（{}）· 输出预留 {}\n\
             触发阈值 {}（越过后该 Compact，P2 才动手）\n\
             这是估算，不是上游计数 —— 括注里的实测值才是。",
            usage.ratio,
            usage
                .ctx_len
                .unwrap_or(usage.budget_tokens as i64)
                .to_string(),
            match usage.ctx_len_source {
                microchat::world::CtxLenSource::Override => "你填的覆盖值",
                microchat::world::CtxLenSource::Model => "上游发现",
                microchat::world::CtxLenSource::Setting => "设置里的兜底",
            },
            usage
                .max_output
                .map(|v| v.to_string())
                .unwrap_or_else(|| "缺省 4096".to_owned()),
            usage.trigger_tokens,
        );
        ui.horizontal(|ui| {
            ui.colored_label(color, RichText::new(line).small())
                .on_hover_text(hover);
        });
        ui.add_space(4.0);
    }
}

impl App {
    /// 会话最上面那块：这次对话**真正会用到的系统提示词**（会话级覆盖优先，否则 agent 的）。
    ///
    /// 默认只露几行——它常常是整篇世界观，全摊开会把消息挤下去；要看全得按「展开」。
    /// 取 `&self`：调用点在消息闭包里，那里借不到可变的 self，展开动作走 `toggle` 带出去。
    pub(super) fn system_prompt_row(
        &self,
        ui: &mut egui::Ui,
        agent_label: &str,
        prompt: Option<&str>,
        toggle: &mut bool,
    ) {
        egui::Frame::NONE
            .fill(ui.visuals().extreme_bg_color)
            .stroke(ui.visuals().widgets.noninteractive.bg_stroke)
            .corner_radius(egui::CornerRadius::same(6))
            .inner_margin(egui::Margin::same(8))
            .show(ui, |ui| {
                ui.horizontal(|ui| {
                    ui.colored_label(
                        ui.visuals().weak_text_color(),
                        RichText::new(format!("系统提示词 · {agent_label}")).strong(),
                    );
                    ui.with_layout(Layout::right_to_left(Align::Center), |ui| {
                        if ui
                            .small_button(if self.system_prompt_open { "收起" } else { "展开" })
                            .clicked()
                        {
                            *toggle = true;
                        }
                    });
                });
                ui.add_space(4.0);

                let Some(prompt) = prompt else {
                    ui.label(
                        RichText::new("这条会话没有系统提示词：会话与 agent 都没配，模型收到的只有历史").weak(),
                    );
                    return;
                };
                if self.system_prompt_open {
                    ui.label(RichText::new(prompt).weak());
                    ui.add_space(2.0);
                    ui.label(RichText::new("发送时还会在后面注入当前世界状态表").weak().small());
                } else {
                    ui.label(RichText::new(summarize_lines(prompt)).weak());
                    let lines = prompt.lines().count();
                    if lines > SYSTEM_PROMPT_PREVIEW_LINES {
                        ui.label(
                            RichText::new(format!("（共 {lines} 行，点「展开」看全文）"))
                                .weak()
                                .small(),
                        );
                    }
                }
            });
        ui.add_space(6.0);
    }
}

impl App {
    pub(super) fn chat_view(&mut self, ui: &mut egui::Ui, composing: bool, enter: bool) {
        let Some(current) = self.current else {
            ui.centered_and_justified(|ui| {
                ui.label(RichText::new("还没有会话：点左上角「＋ 新建对话」").weak());
            });
            return;
        };
        // 两个来源都算"忙"：本地那个（POST 还没回）与后端的状态（生成还在跑）
        let turn_busy = self
            .turn
            .as_ref()
            .is_some_and(|(conversation, status)| *conversation == current && status.phase.is_busy());
        let sending = self.pending_send == Some(current) || turn_busy;

        // 会话上方一条：当前模型与 Agent，都能直接在下拉里换（换完 PATCH 回后端）。
        egui::Panel::top("chat_header").show(ui, |ui| {
            ui.add_space(4.0);
            ui.horizontal(|ui| {
                let conversation = self.conversations.iter().find(|c| c.id == current).cloned();
                let (provider, model, agent_id) = match &conversation {
                    Some(c) => (c.provider.clone(), c.model.clone(), c.agent_id.clone()),
                    None => (String::new(), String::new(), String::new()),
                };

                ui.label(RichText::new("模型").weak());
                self.model_picker(ui, current, &provider, &model);

                ui.separator();

                ui.label(RichText::new("Agent").weak());
                let mut picked_agent: Option<String> = None;
                egui::ComboBox::from_id_salt("chat_agent")
                    .selected_text(RichText::new(self.agent_label(&agent_id)).monospace())
                    .width(180.0)
                    .show_ui(ui, |ui| {
                        for agent in self.agents.iter().flat_map(|config| config.agents.iter()) {
                            let label = if agent.name.is_empty() {
                                agent.id.clone()
                            } else {
                                agent.name.clone()
                            };
                            if ui.selectable_label(agent.id == agent_id, label).clicked() {
                                picked_agent = Some(agent.id.clone());
                            }
                        }
                    });
                if let Some(agent_id) = picked_agent {
                    self.switch_agent(current, agent_id);
                }
            });
            ui.add_space(4.0);
        });

        egui::Panel::bottom("composer").show(ui, |ui| {
            // 会话级的两个动作：归档（剪枝前必做）与复制会话（真分支）
            ui.horizontal(|ui| {
                let (base, token) = (self.settings.server_address.clone(), self.token.clone());
                if ui
                    .small_button("归档")
                    .on_hover_text("把这条会话（含全部分支）写成 data/archive/*.json —— 剪枝前的必做动作")
                    .clicked()
                {
                    self.client.send(Command::ArchiveConversation {
                        base: base.clone(),
                        token: token.clone(),
                        conversation: current,
                    });
                }
                // 手动压缩：调试期用 dummy 跑（不联网），先拿它把整条链路走顺
                ui.separator();
                ui.label(RichText::new("压缩").weak());
                ui.add(
                    egui::TextEdit::singleline(&mut self.compact_blocks)
                        .desired_width(40.0),
                )
                .on_hover_text("尝试压缩几个【对话块】（一个块 = 一轮问答；最后那个开着的块永不压）");
                ui.label(RichText::new("个块").weak());
                if ui
                    .small_button("开始")
                    .on_hover_text("把最老的 N 个块收成一条摘要（202 受理，进度看底栏）")
                    .clicked()
                {
                    match self.compact_blocks.trim().parse::<usize>() {
                        Ok(blocks) if blocks > 0 => {
                            self.client.send(Command::Compact {
                                base: base.clone(),
                                token: token.clone(),
                                conversation: current,
                                blocks,
                            });
                        }
                        _ => self.note = "「压缩」要填一个正整数".to_owned(),
                    }
                }
                ui.separator();
                if ui
                    .small_button("复制会话")
                    .on_hover_text("复制出一条新会话（真分支用它；会话内那点分支只为重摇）")
                    .clicked()
                {
                    self.client.send(Command::ForkConversation {
                        base: base.clone(),
                        token: token.clone(),
                        conversation: current,
                    });
                }
                ui.label(
                    RichText::new("归档留底 · 复制会话开新局")
                        .weak()
                        .small(),
                );
            });
            ui.add_space(8.0);
            ui.horizontal(|ui| {
                let hint = if self.settings.enter_sends {
                    "输入消息…  Enter 发送 / Shift+Enter 换行"
                } else {
                    "输入消息…  Shift+Enter 换行"
                };
                let hint_text = hint;
                let width = (ui.available_width() - 88.0).max(80.0);
                let draft = self.drafts.entry(current).or_default();
                let edit = ui.add_sized(
                    [width, 88.0],
                    TextEdit::multiline(draft)
                        .desired_rows(3)
                        .hint_text(hint_text),
                );
                self.composer_id = Some(edit.id);
                if self.focus_pending {
                    edit.request_focus();
                    self.focus_pending = false;
                }
                if edit.has_focus() && self.preedit_active {
                    ui.ctx().request_repaint();
                }
                // 右键菜单：选区快照 + 剪切/复制/粘贴/全选（见 attach_edit_menu）
                if let Some(draft) = self.drafts.get(&current)
                    && let Some(deferred) =
                        attach_edit_menu(ui, &edit, draft, &mut self.menu_selection, false)
                {
                    self.deferred = Some(deferred);
                }

                let has_text = self
                    .drafts
                    .get(&current)
                    .is_some_and(|draft| !draft.trim().is_empty());
                let send = ui.add_enabled(
                    has_text && !sending,
                    egui::Button::new(if sending { "等待…" } else { "发送" }),
                );
                if !sending
                    && (send.clicked()
                        || (enter && self.settings.enter_sends && !composing && has_text))
                {
                    self.send_message();
                } else if enter && self.settings.enter_sends && !composing && !sending {
                    // multiline 的 Enter 会顺带插入换行，空输入时把它吃掉。
                    if let Some(draft) = self.drafts.get_mut(&current) {
                        draft.clear();
                    }
                }
            });
            ui.add_space(8.0);
        });

        egui::Panel::right("chat_vars")
            .resizable(true)
            .default_size(260.0)
            .show(ui, |ui| self.vars_panel(ui, current));

        let stick = self.settings.stick_to_bottom;
        let messages = self
            .messages
            .get(&current)
            .map(Vec::as_slice)
            .unwrap_or(&[]);
        // 进闭包前把"要显示的名字与提示词"算好：闭包里只借得到 self 的不可变引用。
        let conversation = self.conversations.iter().find(|c| c.id == current).cloned();
        let assistant_label = conversation
            .as_ref()
            .map(|c| self.agent_label(&c.agent_id))
            .unwrap_or_else(|| "助手".to_owned());
        let system_prompt = conversation
            .as_ref()
            .and_then(|c| self.effective_system_prompt(c));
        let mut toggle_system_prompt = false;
        let mut switch_leaf: Option<Uuid> = None;
        // 闭包只借得到不可变的 self，先取出来
        let branches = self.branches.get(&current).cloned().unwrap_or_default();
        // 快照与延迟动作在闭包里只能用局部变量：进来取出来，出去再放回去
        let mut local_selection = self.menu_selection.take();
        let mut deferred: Option<DeferredMenu> = None;
        // 编辑态先从 self 里取出来：下面闭包要同时可变借用草稿，而 `messages` 正借着 self.messages
        let mut editing = self.editing.take();
        let mut start_edit: Option<(Uuid, String)> = None;
        let mut delete_now: Option<Uuid> = None;
        let mut delete_siblings: Option<Uuid> = None;
        let mut resend = false;
        // 这一轮的状态：生成中那条占位消息要显示秒数与「停止」
        let turn = self
            .turn
            .clone()
            .filter(|(conversation, _)| *conversation == current);
        // 闭包里只借得到不可变 self，动画缓冲区先克隆出来
        let streamed = self.streamed.clone();
        let thinking = self.thinking.clone();
        let mut stop_turn = false;
        let mut save_edit: Option<(Uuid, String)> = None;
        let mut cancel_edit = false;
        let mut edit_box_id: Option<egui::Id> = None;
        egui::ScrollArea::vertical()
            .id_salt("chat_messages")
            .auto_shrink([false, false])
            .stick_to_bottom(stick)
            .show(ui, |ui| {
                // 左右留白：消息块不贴边——否则右侧滚动条会压在文字上，看着也憋。
                egui::Frame::NONE
                    .inner_margin(egui::Margin::symmetric(16, 6))
                    .show(ui, |ui| {
                        // 第一句永远是系统提示词：它不在 messages 里（组装出站消息时才注入），
                        // 所以单独排一块，让人看得见"这次对话到底被交代了什么"。
                        // 它上面一行是上下文占用（`上下文 12.3k / 40k`）。
                        self.context_row(ui);
                        self.system_prompt_row(
                            ui,
                            &assistant_label,
                            system_prompt.as_deref(),
                            &mut toggle_system_prompt,
                        );
                        if messages.is_empty() {
                            ui.add_space(6.0);
                            ui.label(RichText::new("还没有消息：在下面的输入框里说第一句").weak());
                        }
                        let last_index = messages.len().saturating_sub(1);
                        for (index, message) in messages.iter().enumerate() {
                            // 角色只靠左上角的名字区分：用户是「用户」，助手是**这个 agent 的名字**
                            // （一个会话一个 agent，所以整列助手都写它，而不是"助手"两个字）。
                            let (label, color) = match message.role {
                                ApiRole::User => {
                                    ("用户".to_owned(), Color32::from_rgb(0x2f, 0x6f, 0xe0))
                                }
                                ApiRole::Assistant => (
                                    assistant_label.clone(),
                                    Color32::from_rgb(0x2f, 0x9e, 0x44),
                                ),
                            };
                            let is_editing = editing
                                .as_ref()
                                .is_some_and(|(id, _)| *id == message.id);

                            // 每条消息是一个块：底色 + 圆角 + 内边距，块与块之间留间距。
                            egui::Frame::NONE
                                .fill(ui.visuals().faint_bg_color)
                                .corner_radius(egui::CornerRadius::same(6))
                                .inner_margin(egui::Margin::same(8))
                                .show(ui, |ui| {
                                    ui.horizontal(|ui| {
                                        ui.colored_label(color, RichText::new(label).strong());
                                        // 分支操作只给**当前最新那句**：重新发送过几次就会有兄弟，
                                        // 给一个「‹ 2/3 ›」切候选，外加「删除全部」一次清光。
                                        // 旧消息上不出现这些——切到旧分支会把对话倒回去，
                                        // 而变量是沿当前路径现演的，世界状态会跟着倒退。
                                        if index != last_index {
                                            // 不是尾巴：什么都不显示
                                        } else if let Some(branch) = branches.get(&message.id) {
                                            if branch.total > 1 {
                                                if ui
                                                    .add_enabled(
                                                        branch.index > 1,
                                                        egui::Button::new("‹").small(),
                                                    )
                                                    .clicked()
                                                {
                                                    switch_leaf =
                                                        branch.siblings.get(branch.index - 2).copied();
                                                }
                                                ui.label(
                                                    RichText::new(format!(
                                                        "{}/{}",
                                                        branch.index, branch.total
                                                    ))
                                                    .weak()
                                                    .small(),
                                                );
                                                if ui
                                                    .add_enabled(
                                                        branch.index < branch.total,
                                                        egui::Button::new("›").small(),
                                                    )
                                                    .clicked()
                                                {
                                                    switch_leaf =
                                                        branch.siblings.get(branch.index).copied();
                                                }
                                                if ui
                                                    .small_button("删除全部")
                                                    .on_hover_text(
                                                        "把这组回复连同它们下面的分支一起删掉，只留下上文",
                                                    )
                                                    .clicked()
                                                {
                                                    delete_siblings = Some(message.id);
                                                }
                                            }
                                        }
                                        ui.with_layout(Layout::right_to_left(Align::Center), |ui| {
                                            if is_editing {
                                                if ui.small_button("保存").clicked() {
                                                    save_edit = editing.clone();
                                                }
                                                if ui.small_button("取消").clicked() {
                                                    cancel_edit = true;
                                                }
                                            } else {
                                                if ui.small_button("编辑").clicked() {
                                                    start_edit =
                                                        Some((message.id, message.content.clone()));
                                                }
                                                let text = message.content.clone();
                                                if ui.small_button("复制").clicked() {
                                                    ui.output_mut(|o| {
                                                        o.commands.push(egui::OutputCommand::CopyText(text))
                                                    });
                                                }
                                                if index == last_index
                                                    && ui
                                                        .small_button("重新发送")
                                                        .on_hover_text(
                                                            "再生成一条。旧的那条会留着，用上面的「‹ 2/3 ›」切回去",
                                                        )
                                                        .clicked()
                                                {
                                                    resend = true;
                                                }
                                                if ui.small_button("删除").clicked() {
                                                    delete_now = Some(message.id);
                                                }
                                            }
                                        });
                                    });

                                    // 模型的"思考"：**只留档**，默认折叠。
                                    // 它不参与任何计算——不进历史、不扫 <state>、不可编辑。
                                    if let Some(reasoning) = message
                                        .reasoning
                                        .as_deref()
                                        .filter(|text| !text.trim().is_empty())
                                    {
                                        // 折叠栏上放**思考用时**（API 按 token 计费，"字"没意义）；
                                        // 迁移之前的老消息没这个数，退回字数——至少给个量级
                                        let label = match message.reasoning_ms {
                                            Some(ms) => format!("思考（{}）", fmt_duration(ms)),
                                            None => format!(
                                                "思考（{} 字）",
                                                reasoning.chars().count()
                                            ),
                                        };
                                        egui::CollapsingHeader::new(
                                            RichText::new(label).weak().small(),
                                        )
                                        .id_salt(message.id)
                                        .default_open(false)
                                        .show(ui, |ui| {
                                            let mut text = reasoning;
                                            ui.add(
                                                TextEdit::multiline(&mut text)
                                                    .frame(egui::Frame::NONE)
                                                    .desired_width(f32::INFINITY),
                                            );
                                        });
                                    }

                                    // 卡片脚注：这一轮花了多久、上下行/缓存各多少 token。
                                    // 只在有数据时显示——dummy、兜底、老消息都没有，不装作有。
                                    if !is_editing && message.role == ApiRole::Assistant {
                                        let mut bits: Vec<String> = Vec::new();
                                        if let Some(ms) = message.duration_ms {
                                            bits.push(fmt_duration(ms));
                                        }
                                        if let Some(usage) = message.usage.as_ref() {
                                            // 单位写清楚：这里是 **token**（流式气泡上的"字"是字符数，两码事）
                                            let cached = if usage.cached_tokens > 0 {
                                                let percent = usage.cached_tokens * 100
                                                    / usage.prompt_tokens.max(1);
                                                format!(
                                                    "（缓存 {} tok · {}%）",
                                                    usage.cached_tokens, percent
                                                )
                                            } else {
                                                String::new()
                                            };
                                            // 思考是"下行"的**子集**，不是另加
                                            let reasoning = if usage.reasoning_tokens > 0 {
                                                format!("（思考 {} tok，含在内）", usage.reasoning_tokens)
                                            } else {
                                                String::new()
                                            };
                                            bits.push(format!(
                                                "上行 {} tok{cached}",
                                                usage.prompt_tokens
                                            ));
                                            bits.push(format!(
                                                "下行 {} tok{reasoning}",
                                                usage.completion_tokens
                                            ));
                                        }
                                        if !bits.is_empty() {
                                            ui.label(RichText::new(bits.join(" · ")).weak().small());
                                        }
                                    }

                                    match editing.as_mut() {
                                        Some((id, draft)) if *id == message.id => {
                                            let box_response = ui.add_sized(
                                                [ui.available_width(), 140.0],
                                                TextEdit::multiline(draft).desired_rows(5),
                                            );
                                            edit_box_id = Some(box_response.id);
                                            if let Some(picked) = attach_edit_menu(
                                                ui,
                                                &box_response,
                                                draft,
                                                &mut local_selection,
                                                false,
                                            ) {
                                                deferred = Some(picked);
                                            }
                                        }
                                        _ => {
                                            // 只读文本用"绑到不可变 str 的 TextEdit"（egui 官方讨论推荐）：
                                            // 看起来就是一段文字，但能选中、能 Ctrl+C，也能走同一套右键菜单。
                                            let mut text = message.content.as_str();
                                            let response = ui.add(
                                                TextEdit::multiline(&mut text)
                                                    .frame(egui::Frame::NONE)
                                                    .desired_width(f32::INFINITY),
                                            );
                                            if let Some(picked) = attach_edit_menu(
                                                ui,
                                                &response,
                                                &message.content,
                                                &mut local_selection,
                                                true,
                                            ) {
                                                deferred = Some(picked);
                                            }
                                        }
                                    }
                                });
                            ui.add_space(12.0);
                        }

                        // 生成中的那条回复**库里还没有**（拿到整段才 INSERT），所以按状态合成
                        // 一个气泡挂在末尾。落库后它的 id 会出现在消息列表里（同一个 id），
                        // 这个气泡自然消失。
                        if let Some((_, status)) = turn.as_ref().filter(|(_, status)| {
                            status
                                .message_id
                                .is_some_and(|id| messages.iter().all(|message| message.id != id))
                        }) {
                            let elapsed = status.elapsed_ms as f64 / 1000.0;
                            egui::Frame::NONE
                                .fill(ui.visuals().faint_bg_color)
                                .corner_radius(egui::CornerRadius::same(6))
                                .inner_margin(egui::Margin::same(8))
                                .show(ui, |ui| {
                                    ui.horizontal(|ui| {
                                        ui.colored_label(
                                            Color32::from_rgb(0x2f, 0x9e, 0x44),
                                            RichText::new(&assistant_label).strong(),
                                        );
                                        ui.with_layout(Layout::right_to_left(Align::Center), |ui| {
                                            if ui.small_button("停止").clicked() {
                                                stop_turn = true;
                                            }
                                        });
                                    });
                                    ui.horizontal(|ui| {
                                        ui.spinner();
                                        ui.label(
                                            RichText::new(format!("正在生成… {elapsed:.1}s")).weak(),
                                        );
                                    });
                                    if !thinking.is_empty() {
                                        // 推理型模型先"想"一段：把它显示出来，
                                        // 否则那十几秒界面看起来像死着（它不进最终消息）。
                                        // 只报"在想"：秒数上面那行本来就在动，
                                        // 而"字"既不是计费单位也不是进度（token 要等末帧 usage）。
                                        ui.label(RichText::new("思考中…").weak().small());
                                        let mut text = thinking.as_str();
                                        ui.add(
                                            TextEdit::multiline(&mut text)
                                                .frame(egui::Frame::NONE)
                                                .desired_width(f32::INFINITY),
                                        );
                                    }
                                    if !streamed.is_empty() {
                                        // 逐字长出来的正文：**这是动画**。真消息落库后由它接管
                                        // （同一个 id，气泡自然消失）；它不进树、不参与变量计算。
                                        let mut text = streamed.as_str();
                                        ui.add(
                                            TextEdit::multiline(&mut text)
                                                .frame(egui::Frame::NONE)
                                                .desired_width(f32::INFINITY),
                                        );
                                    }
                                });
                        }
                    });
            });

        if let Some(pair) = start_edit {
            editing = Some(pair);
        }
        if cancel_edit {
            editing = None;
        }
        self.menu_selection = local_selection;
        if let Some(deferred) = deferred {
            self.deferred = Some(deferred);
        }
        if let Some((message, content)) = save_edit {
            editing = None;
            let (base, token) = (self.settings.server_address.clone(), self.token.clone());
            self.client.send(Command::EditMessage {
                base,
                token,
                conversation: current,
                message,
                content,
            });
        }
        if toggle_system_prompt {
            self.system_prompt_open = !self.system_prompt_open;
        }
        if let Some(id) = delete_now {
            let (base, token) = (self.settings.server_address.clone(), self.token.clone());
            self.client.send(Command::DeleteMessage {
                base,
                token,
                conversation: current,
                message: id,
            });
        }
        if let Some(id) = delete_siblings {
            let (base, token) = (self.settings.server_address.clone(), self.token.clone());
            self.client.send(Command::DeleteSiblings {
                base,
                token,
                conversation: current,
                message: id,
            });
        }
        if resend {
            let (base, token) = (self.settings.server_address.clone(), self.token.clone());
            self.client.send(Command::Resend {
                base,
                token,
                conversation: current,
            });
        }
        if stop_turn {
            let (base, token) = (self.settings.server_address.clone(), self.token.clone());
            self.client.send(Command::StopTurn {
                base,
                token,
                conversation: current,
            });
        }
        if let Some(leaf) = switch_leaf {
            let (base, token) = (self.settings.server_address.clone(), self.token.clone());
            self.client.send(Command::SetLeaf {
                base,
                token,
                conversation: current,
                leaf,
            });
        }
        self.editing = editing;
    }
}
