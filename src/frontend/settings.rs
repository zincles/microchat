//! settings：从原来的 main.rs 拆出来的（纯搬家，零行为变更）。

use super::*;

impl App {
    /// 连接页：未连上后端时唯一能看到的东西。
    pub(super) fn connect_screen(&mut self, ui: &mut egui::Ui) {
        ui.vertical_centered(|ui| {
            ui.add_space(48.0);
            ui.heading("microchat");
            ui.label(RichText::new("需要连接后端才能使用").weak());
            ui.add_space(24.0);
        });

        ui.vertical_centered(|ui| {
            ui.allocate_ui_with_layout(
                egui::vec2(420.0, 180.0),
                Layout::top_down(Align::Min),
                |ui| {
                    egui::Grid::new("connect_form")
                        .num_columns(2)
                        .spacing([12.0, 10.0])
                        .show(ui, |ui| {
                            ui.label("服务器地址");
                            let edit_response = ui.add(
                                TextEdit::singleline(&mut self.settings.server_address)
                                    .desired_width(280.0),
                            );
                            if let Some(deferred) = attach_edit_menu(
                                ui,
                                &edit_response,
                                &self.settings.server_address,
                                &mut self.menu_selection,
                                false,
                            ) {
                                self.deferred = Some(deferred);
                            }
                            if edit_response.changed() {
                                self.settings_dirty = true;
                            }
                            ui.end_row();

                            ui.label("用户名");
                            let edit_response = ui.add(
                                TextEdit::singleline(&mut self.settings.username)
                                    .desired_width(280.0),
                            );
                            if let Some(deferred) = attach_edit_menu(ui, &edit_response, &self.settings.username, &mut self.menu_selection, false) {
                                self.deferred = Some(deferred);
                            };
                            ui.end_row();

                            ui.label("密码");
                            let edit_response = ui.add(
                                TextEdit::singleline(&mut self.password)
                                    .password(true)
                                    .desired_width(280.0),
                            );
                            if let Some(deferred) = attach_edit_menu(ui, &edit_response, &self.password, &mut self.menu_selection, false) {
                                self.deferred = Some(deferred);
                            };
                            ui.end_row();
                        });

                    ui.add_space(12.0);
                    let checking = self.backend == Backend::Checking;
                    let label = if checking { "连接中…" } else { "连接" };
                    if ui
                        .add_enabled(
                            !checking,
                            egui::Button::new(label).min_size(egui::vec2(96.0, 28.0)),
                        )
                        .clicked()
                    {
                        self.connect();
                    }
                },
            );
        });

        ui.vertical_centered(|ui| {
            ui.add_space(8.0);
            match &self.backend {
                Backend::Checking => {
                    ui.spinner();
                }
                Backend::Offline(message) if !message.is_empty() => {
                    ui.colored_label(Color32::from_rgb(0xd0, 0x4a, 0x4a), message);
                    ui.label(
                        RichText::new(
                            "确认后端已启动：cargo run --bin server（默认 127.0.0.1:8787）",
                        )
                        .weak(),
                    );
                }
                _ => {}
            }
        });
    }
}

impl App {
    /// 服务器设置：连接信息 + agents（可写）+ 模型（可刷新）。
    /// 连接：后端地址、口令状态、断开。
    pub(super) fn settings_connection(&mut self, ui: &mut egui::Ui) {
        ui.label(RichText::new("连接").strong());
        ui.horizontal(|ui| {
            ui.label(RichText::new(&self.settings.server_address).monospace());
            ui.label(
                RichText::new(if self.token.is_some() {
                    "（已带口令）"
                } else {
                    "（无口令）"
                })
                .weak(),
            );
            if ui.button("断开").clicked() {
                self.backend = Backend::Offline("已断开".to_owned());
                self.providers = None;
                self.agents = None;
                self.selected_agent = None;
            }
        });
        ui.separator();
    }
}

impl App {
    /// Agent（预设）：列表、编辑、新建、设为默认。保存走页面右下角的「保存」。
    pub(super) fn settings_agents(&mut self, ui: &mut egui::Ui) {
        ui.label(RichText::new("Agents（预设）").strong());
        ui.label(
            RichText::new("改动会写回后端的 config/agents.json；该文件会被整体重写，注释会丢")
                .weak(),
        );

        let mut select: Option<String> = None;
        let mut delete: Option<String> = None;
        let mut make_default: Option<String> = None;
        match &self.agents {
            None => {
                ui.label(RichText::new("加载中…").weak());
            }
            Some(config) => {
                if config.agents.is_empty() {
                    ui.label(RichText::new("还没有 agent，下面新建一个").weak());
                }
                // 谁是"新建会话默认会用的那个"标出来（空 default_agent 时就是内置默认）
                let default_agent = config.default_agent().map(|agent| agent.id);
                for agent in &config.agents {
                    ui.horizontal(|ui| {
                        let selected = self.selected_agent.as_deref() == Some(agent.id.as_str());
                        let mut label = if agent.name.is_empty() {
                            agent.id.clone()
                        } else {
                            format!("{}（{}）", agent.name, agent.id)
                        };
                        if default_agent.as_deref() == Some(agent.id.as_str()) {
                            label.push_str("  ★默认");
                        }
                        if ui.selectable_label(selected, label).clicked() {
                            select = Some(agent.id.clone());
                        }
                        ui.with_layout(Layout::right_to_left(Align::Center), |ui| {
                            if close_button(ui).clicked() {
                                delete = Some(agent.id.clone());
                            }
                        });
                    });
                }
            }
        }
        if let Some(id) = select {
            self.selected_agent = Some(id);
            self.load_selected_agent();
        }
        if let Some(id) = delete {
            let (base, token) = (self.settings.server_address.clone(), self.token.clone());
            self.client.send(Command::DeleteAgent { base, token, id });
        }

        if self.selected_agent.is_some() {
            ui.add_space(6.0);
            egui::Grid::new("agent_editor")
                .num_columns(2)
                .spacing([12.0, 8.0])
                .show(ui, |ui| {
                    ui.label("id");
                    // 新建时 id 由后端生成（不给指定）；**已存在的可以改**，等于重命名：
                    // `default_agent` 与所有会话的引用会跟着一起搬，所以是安全的。
                    let edit_response =
                        ui.add(TextEdit::singleline(&mut self.agent_id).desired_width(280.0));
                    if let Some(deferred) = attach_edit_menu(
                        ui,
                        &edit_response,
                        &self.agent_id,
                        &mut self.menu_selection,
                        false,
                    ) {
                        self.deferred = Some(deferred);
                    };
                    ui.end_row();

                    ui.label("名称");
                    let edit_response = ui.add(TextEdit::singleline(&mut self.agent_name).desired_width(280.0));
                    if let Some(deferred) = attach_edit_menu(ui, &edit_response, &self.agent_name, &mut self.menu_selection, false) {
                        self.deferred = Some(deferred);
                    };
                    ui.end_row();

                    ui.label("系统提示词");
                    let edit_response = ui.add(
                        TextEdit::multiline(&mut self.agent_prompt)
                            .desired_rows(6)
                            .desired_width(420.0),
                    );
                    if let Some(deferred) = attach_edit_menu(ui, &edit_response, &self.agent_prompt, &mut self.menu_selection, false) {
                        self.deferred = Some(deferred);
                    };
                    ui.end_row();
                });
            let is_default = self
                .agents
                .as_ref()
                .and_then(|config| config.default_agent())
                .is_some_and(|agent| agent.id == self.agent_id);
            ui.horizontal(|ui| {
                if ui
                    .add_enabled(!is_default, egui::Button::new("设为默认 agent"))
                    .on_hover_text("新建会话默认用它（写进 agents.json 的 default_agent）")
                    .clicked()
                {
                    make_default = Some(self.agent_id.clone());
                }
                if is_default {
                    ui.label(RichText::new("当前就是默认").weak());
                }
            });
            ui.label(RichText::new("改完点右下角「保存」提交").weak());
        }

        if let Some(id) = make_default {
            let (base, token) = (self.settings.server_address.clone(), self.token.clone());
            self.client.send(Command::MakeDefaultAgent { base, token, id });
        }

        ui.add_space(8.0);
        ui.separator();
        ui.label(RichText::new("新建 agent").strong());
        ui.horizontal(|ui| {
            ui.label("名称");
            let edit_response = ui.add(TextEdit::singleline(&mut self.new_agent_name).desired_width(160.0));
            if let Some(deferred) = attach_edit_menu(ui, &edit_response, &self.new_agent_name, &mut self.menu_selection, false) {
                self.deferred = Some(deferred);
            };
            let ready = !self.new_agent_name.trim().is_empty();
            if ui.add_enabled(ready, egui::Button::new("新建")).clicked() {
                let (base, token) = (self.settings.server_address.clone(), self.token.clone());
                self.client.send(Command::CreateAgent {
                    base,
                    token,
                    name: self.new_agent_name.trim().to_owned(),
                    system_prompt: String::new(),
                });
                self.new_agent_name.clear();
            }
        });

        ui.add_space(8.0);
        ui.separator();
    }
}

impl App {
    /// 能力：写死在代码里的**固定流程**（目前只有摘要器）。名字与内置模板来自代码，这里只改怎么用它。
    ///
    /// 名字、说明、内置模板都**来自代码**（加工具＝改 Rust）；这一页只改它们的
    /// 模板 / 渠道 / 模型。清空模板 = 用内置；保存走页脚那个按钮（与 Agent、模型页一致）。
    pub(super) fn settings_abilities(&mut self, ui: &mut egui::Ui) {
        ui.label(RichText::new("能力").strong());
        ui.label(
            RichText::new(
                "能力（Ability）是写死在代码里的固定流程：名字、说明、内置模板都在代码里（想加一个＝改 Rust）。\
                 这里只改它**怎么用**：模板留空 = 用内置；渠道/模型留空 = 跟随会话。",
            )
            .weak(),
        );
        ui.add_space(8.0);

        let Some(abilities) = self.abilities.clone() else {
            ui.label(RichText::new("加载中…").weak());
            return;
        };

        // 已知的「渠道 · 模型」候选（下拉用）；读一次，循环里不再碰 self.providers
        let model_rows = flat_model_rows(self.providers.as_deref().unwrap_or(&[]));
        let follow_label = "跟随会话（用会话自己的渠道与模型）".to_owned();

        let mut restore_builtin: Option<String> = None;
        let mut picked_route: Option<(String, String, String)> = None; // (id, provider, model)
        for ability in &abilities {
            let Some((prompt, provider, model)) = self.ability_drafts.get_mut(&ability.id) else {
                continue;
            };
            let using_builtin = prompt.trim() == ability.builtin_prompt.trim();
            egui::Frame::NONE
                .fill(ui.visuals().extreme_bg_color)
                .stroke(ui.visuals().widgets.noninteractive.bg_stroke)
                .corner_radius(egui::CornerRadius::same(6))
                .inner_margin(egui::Margin::same(8))
                .show(ui, |ui| {
                    ui.horizontal(|ui| {
                        ui.label(RichText::new(&ability.name).strong());
                        ui.label(RichText::new(format!("（{}）", ability.id)).weak().small());
                        ui.label(
                            RichText::new(if using_builtin { "· 内置模板" } else { "· 已改过" })
                                .weak()
                                .small(),
                        );
                        ui.label(
                            RichText::new(format!("版本 {}", ability.prompt_version))
                                .weak()
                                .small(),
                        )
                        .on_hover_text("生效模板的哈希短号：改了模板它自己就变，落库时记进摘要");
                        ui.with_layout(Layout::right_to_left(Align::Center), |ui| {
                            if ui
                                .small_button("恢复内置")
                                .on_hover_text("把模板恢复成代码里的那份（保存后才生效）")
                                .clicked()
                            {
                                restore_builtin = Some(ability.id.clone());
                            }
                        });
                    });
                    ui.label(RichText::new(&ability.about).weak().small());
                    // 渠道与模型合成一个下拉（和会话页同一个做法）：跟随会话，或挑一个已知模型
                    ui.horizontal(|ui| {
                        ui.label("渠道与模型");
                        let selected_text = if provider.is_empty() && model.is_empty() {
                            follow_label.clone()
                        } else {
                            format!(
                                "{} · {}",
                                if provider.is_empty() { "（跟随会话的渠道）" } else { provider.as_str() },
                                if model.is_empty() { "跟随会话" } else { model.as_str() }
                            )
                        };
                        egui::ComboBox::from_id_salt(("subagent_route", &ability.id))
                            .selected_text(RichText::new(selected_text).monospace())
                            .width(360.0)
                            .show_ui(ui, |ui| {
                                egui::ScrollArea::vertical().max_height(360.0).show(ui, |ui| {
                                    let following = provider.is_empty() && model.is_empty();
                                    if ui
                                        .selectable_label(following, follow_label.clone())
                                        .clicked()
                                    {
                                        picked_route = Some((
                                            ability.id.clone(),
                                            String::new(),
                                            String::new(),
                                        ));
                                    }
                                    for (label, provider_id, model_id) in &model_rows {
                                        let selected =
                                            provider == provider_id && model == model_id;
                                        if ui.selectable_label(selected, label).clicked() {
                                            picked_route = Some((
                                                ability.id.clone(),
                                                provider_id.clone(),
                                                model_id.clone(),
                                            ));
                                        }
                                    }
                                });
                            });
                        ui.label(
                            RichText::new("压缩是体力活，可以挑个便宜的")
                                .weak()
                                .small(),
                        );
                    });
                    ui.add_space(4.0);

                    // 可用占位符：**逐条列出含义**（白名单写死在代码里）
                    egui::CollapsingHeader::new("可用占位符（写死在代码里）")
                        .default_open(true)
                        .show(ui, |ui| {
                        egui::Grid::new(("subagent_placeholders", &ability.id))
                            .num_columns(2)
                            .spacing([14.0, 4.0])
                            .show(ui, |ui| {
                                for placeholder in &ability.placeholders {
                                    ui.label(
                                        RichText::new(format!("{{{{{}}}}}", placeholder.name))
                                            .monospace()
                                            .small(),
                                    );
                                    ui.label(RichText::new(&placeholder.about).weak().small());
                                    ui.end_row();
                                }
                            });
                        if !ability.unknown_vars.is_empty() {
                            let names: Vec<String> = ability
                                .unknown_vars
                                .iter()
                                .map(|name| format!("{{{{{name}}}}}"))
                                .collect();
                            ui.label(
                                RichText::new(format!(
                                    "⚠ 这个模板里有不认识的变量：{}（原样留着，不会被替换）",
                                    names.join(" ")
                                ))
                                .color(egui::Color32::from_rgb(210, 170, 70))
                                .small(),
                            );
                        }
                        });
                    ui.add_space(4.0);
                    ui.add(
                        TextEdit::multiline(prompt)
                            .desired_rows(6)
                            .code_editor()
                            .hint_text("留空 = 用内置模板"),
                    );
                });
            ui.add_space(10.0);
        }

        if let Some((id, provider, model)) = picked_route
            && let Some((_, draft_provider, draft_model)) = self.ability_drafts.get_mut(&id)
        {
            *draft_provider = provider;
            *draft_model = model;
        }
        if let Some(id) = restore_builtin
            && let Some(found) = abilities.iter().find(|ability| ability.id == id)
            && let Some((prompt, _, _)) = self.ability_drafts.get_mut(&id)
        {
            *prompt = found.builtin_prompt.to_owned();
        }
    }
}

impl App {
    /// 模型与渠道：provider 清单、刷新模型、编辑连接信息、新建 provider。
    pub(super) fn settings_models(&mut self, ui: &mut egui::Ui) {
        ui.label(RichText::new("模型").strong());
        ui.label(
            RichText::new("「获取模型」会从上游拉取可用模型；成功后本 provider 的模型列表＝上游当前那份")
                .weak(),
        );

        // ── 服务端上下文口径：模型上下文（兜底）+ 摘要触发阈值 ──
        // 草稿先取出来：下面那段借用着 `self.providers`，这里不能再可变借用 self。
        let mut drafts = std::mem::take(&mut self.model_context_drafts);
        ui.add_space(8.0);
        ui.label(RichText::new("服务端：上下文口径").strong());
        egui::Grid::new("chat_config_grid")
            .num_columns(3)
            .spacing([14.0, 6.0])
            .show(ui, |ui| {
                ui.label("模型上下文");
                ui.add(
                    egui::TextEdit::singleline(&mut self.chat_model_context)
                        .desired_width(120.0)
                        .hint_text("兜底 token 数"),
                )
                .on_hover_text("上游没报 context_length 时用这个（例：131072）");
                ui.label(
                    RichText::new("上游没报时的兜底；单个模型可在下面那行覆盖")
                        .weak()
                        .small(),
                );
                ui.end_row();

                ui.label("摘要触发阈值");
                ui.add(
                    egui::TextEdit::singleline(&mut self.chat_trigger)
                        .desired_width(120.0)
                        .hint_text("留空 = 预算 × 0.8"),
                )
                .on_hover_text("占用越过它就该 Compact（P2 才动手）");
                ui.label(
                    RichText::new("留空即用预算的 80%；到点只提示，不动手")
                        .weak()
                        .small(),
                );
                ui.end_row();

                ui.label("");
                ui.label(
                    RichText::new("改完点右下角「保存」——整页草稿一起提交")
                        .weak()
                        .small(),
                );
                ui.label("");
                ui.end_row();
            });
        ui.add_space(6.0);
        ui.separator();

        match &self.providers {
            None => {
                ui.label(RichText::new("加载中…").weak());
            }
            Some(list) if list.is_empty() => {
                ui.label(RichText::new("还没有 provider，下面新建一个").weak());
            }
            Some(list) => {
                let mut refresh: Option<String> = None;
                let mut clear_models: Option<String> = None;
                let mut edit: Option<(String, String, BTreeMap<String, String>)> = None;
                for provider in list {
                    ui.horizontal(|ui| {
                        ui.label(RichText::new(&provider.id).strong());
                        ui.label(RichText::new(kind_label(provider.kind)).weak());
                        ui.label(
                            RichText::new(format!("{} 个模型", provider.models.len())).weak(),
                        );
                        let is_upstream = provider.kind == ProviderKind::OpenAiCompat;
                        if ui
                            .add_enabled(is_upstream, egui::Button::new("获取模型"))
                            .clicked()
                        {
                            refresh = Some(provider.id.clone());
                        }
                        if ui
                            .add_enabled(
                                !provider.models.is_empty(),
                                egui::Button::new("删除全部模型"),
                            )
                            .on_hover_text(
                                "清掉这个渠道已发现的模型：不动 providers.json，也不动历史会话；\
                                 「获取模型」会重新拉一份",
                            )
                            .clicked()
                        {
                            clear_models = Some(provider.id.clone());
                        }
                        if ui.button("编辑").clicked() {
                            edit = Some((
                                provider.id.clone(),
                                provider.base_url.clone(),
                                provider.headers.clone(),
                            ));
                        }
                    });
                    if provider.kind == ProviderKind::OpenAiCompat {
                        ui.horizontal(|ui| {
                            ui.label(RichText::new(&provider.base_url).weak().monospace());
                            ui.label(
                                RichText::new(if provider.has_key {
                                    "已配密钥"
                                } else {
                                    "未配密钥"
                                })
                                .weak(),
                            );
                        });
                    }
                    egui::Grid::new(("models_grid", &provider.id))
                        .num_columns(5)
                        .spacing([12.0, 5.0])
                        .striped(true)
                        .show(ui, |ui| {
                            ui.label(RichText::new("模型").weak().small());
                            ui.label(RichText::new("上游 id").weak().small());
                            ui.label(RichText::new("上下文").weak().small());
                            ui.label(RichText::new("覆盖").weak().small());
                            ui.label("");
                            ui.end_row();

                            for model in &provider.models {
                                ui.label(&model.name);
                                ui.label(RichText::new(&model.upstream_id).weak().monospace().small());
                                // 上游给了就附注大小；没给就说清"在用兜底"；有覆盖就显覆盖
                                match (model.context_override, model.context_length) {
                                    (Some(value), _) => {
                                        ui.label(
                                            RichText::new(short_tokens(value))
                                                .color(egui::Color32::from_rgb(120, 170, 120)),
                                        )
                                        .on_hover_text("你填的覆盖值");
                                    }
                                    (None, Some(value)) => {
                                        ui.label(RichText::new(short_tokens(value)).weak())
                                            .on_hover_text("上游发现所得");
                                    }
                                    (None, None) => {
                                        ui.label(RichText::new("—").weak())
                                            .on_hover_text("上游没报，用上面的兜底");
                                    }
                                }
                                let key = (provider.id.clone(), model.upstream_id.clone());
                                let draft = drafts.entry(key.clone()).or_insert_with(|| {
                                    model
                                        .context_override
                                        .map(|value| value.to_string())
                                        .unwrap_or_default()
                                });
                                ui.add(
                                    egui::TextEdit::singleline(draft)
                                        .desired_width(96.0)
                                        .hint_text("改这里"),
                                );
                                ui.end_row();
                            }
                        });
                    ui.add_space(10.0);
                }
                if let Some(provider) = refresh {
                    let (base, token) = (self.settings.server_address.clone(), self.token.clone());
                    // 记一笔：列表事件回来时才知道这是"获取模型"的结果
                    self.refreshing = Some(provider.clone());
                    self.client.send(Command::RefreshProvider {
                        base,
                        token,
                        provider,
                    });
                }
                if let Some(provider) = clear_models {
                    let (base, token) = (self.settings.server_address.clone(), self.token.clone());
                    self.client.send(Command::ClearProviderModels {
                        base,
                        token,
                        provider,
                    });
                }
                if let Some((id, base_url, headers)) = edit {
                    self.editing_provider = Some(id);
                    self.provider_base_url = base_url;
                    self.provider_headers = header_lines(&headers);
                    self.provider_api_key.clear();
                }
            }
        }
        self.model_context_drafts = drafts;

        ui.add_space(8.0);
        ui.separator();
        ui.label(RichText::new("新建 provider").strong());
        ui.horizontal(|ui| {
            ui.label("id");
            let edit_response = ui.add(TextEdit::singleline(&mut self.new_provider_id).desired_width(120.0));
            if let Some(deferred) = attach_edit_menu(ui, &edit_response, &self.new_provider_id, &mut self.menu_selection, false) {
                self.deferred = Some(deferred);
            };
            ui.label("接口类型");
            for kind in [ProviderKind::OpenAiCompat, ProviderKind::Dummy] {
                ui.selectable_value(&mut self.new_provider_kind, kind, kind_label(kind));
            }
        });

        // 按类型显示待填参数
        match self.new_provider_kind {
            ProviderKind::OpenAiCompat => {
                ui.horizontal(|ui| {
                    ui.label("base_url");
                    let edit_response = ui.add(
                        TextEdit::singleline(&mut self.new_provider_base_url)
                            .desired_width(300.0)
                            .hint_text("https://…/v1"),
                    );
                    if let Some(deferred) = attach_edit_menu(ui, &edit_response, &self.new_provider_base_url, &mut self.menu_selection, false) {
                        self.deferred = Some(deferred);
                    };
                });
                ui.horizontal(|ui| {
                    ui.label("api_key");
                    let edit_response = ui.add(
                        TextEdit::singleline(&mut self.new_provider_api_key)
                            .password(true)
                            .desired_width(300.0)
                            .hint_text("写进后端 providers.json 的 api_key，不回显"),
                    );
                    if let Some(deferred) = attach_edit_menu(ui, &edit_response, &self.new_provider_api_key, &mut self.menu_selection, false) {
                        self.deferred = Some(deferred);
                    };
                });
                ui.horizontal(|ui| {
                    ui.label("headers");
                    let edit_response = ui.add(
                        TextEdit::multiline(&mut self.new_provider_headers)
                            .desired_rows(2)
                            .desired_width(300.0)
                            .hint_text("每行一个：Header: 值（可空）"),
                    );
                    if let Some(deferred) = attach_edit_menu(ui, &edit_response, &self.new_provider_headers, &mut self.menu_selection, false) {
                        self.deferred = Some(deferred);
                    };
                });
            }
            ProviderKind::Dummy => {
                ui.label(
                    RichText::new("dummy 类型没有上游，也不需要 base_url；对话会直接走兜底回复。")
                        .weak(),
                );
            }
        }

        ui.horizontal(|ui| {
            let ready = !self.new_provider_id.trim().is_empty();
            if ui.add_enabled(ready, egui::Button::new("创建")).clicked() {
                let (base, token) = (self.settings.server_address.clone(), self.token.clone());
                let api_key = (!self.new_provider_api_key.is_empty())
                    .then(|| self.new_provider_api_key.clone());
                self.client.send(Command::CreateProvider {
                    base,
                    token,
                    id: self.new_provider_id.trim().to_owned(),
                    kind: self.new_provider_kind,
                    base_url: self.new_provider_base_url.trim().to_owned(),
                    headers: parse_header_lines(&self.new_provider_headers),
                    api_key,
                });
            }
            ui.label(RichText::new(&self.note).weak());
        });

        if let Some(id) = self.editing_provider.clone() {
            ui.add_space(8.0);
            ui.separator();
            ui.label(RichText::new(format!("编辑 provider：{id}")).strong());
            ui.label(
                RichText::new(
                    "id 不可改（历史会话与默认配置都按它引用）。保存会重写 providers.json；密钥就写在这个文件里的 api_key（整个 config/ 不进版本库）",
                )
                .weak(),
            );
            egui::Grid::new("provider_editor")
                .num_columns(2)
                .spacing([12.0, 8.0])
                .show(ui, |ui| {
                    ui.label("base_url");
                    let edit_response = ui.add(TextEdit::singleline(&mut self.provider_base_url).desired_width(320.0));
                    if let Some(deferred) = attach_edit_menu(ui, &edit_response, &self.provider_base_url, &mut self.menu_selection, false) {
                        self.deferred = Some(deferred);
                    };
                    ui.end_row();

                    ui.label("headers");
                    let edit_response = ui.add(
                        TextEdit::multiline(&mut self.provider_headers)
                            .desired_rows(3)
                            .desired_width(320.0)
                            .hint_text("每行一个：Header: 值"),
                    );
                    if let Some(deferred) = attach_edit_menu(ui, &edit_response, &self.provider_headers, &mut self.menu_selection, false) {
                        self.deferred = Some(deferred);
                    };
                    ui.end_row();

                    ui.label("密钥");
                    ui.horizontal(|ui| {
                        let edit_response = ui.add(
                            TextEdit::singleline(&mut self.provider_api_key)
                                .password(true)
                                .desired_width(240.0)
                                .hint_text("留空 = 不改"),
                        );
                        if let Some(deferred) = attach_edit_menu(ui, &edit_response, &self.provider_api_key, &mut self.menu_selection, false) {
                            self.deferred = Some(deferred);
                        };
                        if ui.small_button("清除").clicked() {
                            let (base, token) =
                                (self.settings.server_address.clone(), self.token.clone());
                            self.client.send(Command::UpdateProvider {
                                base,
                                token,
                                id: id.clone(),
                                base_url: self.provider_base_url.trim().to_owned(),
                                headers: parse_header_lines(&self.provider_headers),
                                api_key: Some(String::new()),
                            });
                        }
                    });
                    ui.end_row();
                });
            ui.horizontal(|ui| {
                if ui.button("取消").clicked() {
                    self.editing_provider = None;
                }
                ui.label(RichText::new("改完点右下角「保存」提交").weak());
            });
        }

        if !self.note.is_empty() {
            ui.add_space(8.0);
            ui.label(RichText::new(&self.note).weak());
        }
    }
}

impl App {
    /// 前端设置：只存本机；**改的是草稿**，点右下角「保存」才应用。
    pub(super) fn frontend_settings(&mut self, ui: &mut egui::Ui) {
        ui.label(RichText::new("以下设置只存在本机（frontend.json），不上传后端").weak());
        ui.label(
            RichText::new("改动先落在草稿里，点右下角「保存」才生效——主题与缩放在保存那一刻应用")
                .weak(),
        );
        ui.add_space(6.0);
        egui::Grid::new("frontend_form")
            .num_columns(2)
            .spacing([16.0, 10.0])
            .show(ui, |ui| {
                ui.label("主题");
                ui.horizontal(|ui| {
                    for theme in [Theme::System, Theme::Light, Theme::Dark] {
                        ui.selectable_value(&mut self.frontend_draft.theme, theme, theme.label());
                    }
                });
                ui.end_row();

                ui.label("界面缩放");
                ui.add(
                    egui::Slider::new(&mut self.frontend_draft.zoom, 0.8..=1.5).fixed_decimals(2),
                );
                ui.end_row();

                ui.label("发送");
                ui.checkbox(&mut self.frontend_draft.enter_sends, "Enter 发送消息");
                ui.end_row();

                ui.label("滚动");
                ui.checkbox(&mut self.frontend_draft.stick_to_bottom, "新消息自动滚到底部");
                ui.end_row();

                ui.label("服务器地址");
                let edit_response = ui.add(
                    TextEdit::singleline(&mut self.frontend_draft.server_address)
                        .desired_width(280.0),
                );
                if let Some(deferred) = attach_edit_menu(ui, &edit_response, &self.frontend_draft.server_address, &mut self.menu_selection, false) {
                    self.deferred = Some(deferred);
                };
                ui.end_row();

                ui.label("用户名");
                let edit_response = ui.add(
                    TextEdit::singleline(&mut self.frontend_draft.username).desired_width(280.0),
                );
                if let Some(deferred) = attach_edit_menu(ui, &edit_response, &self.frontend_draft.username, &mut self.menu_selection, false) {
                    self.deferred = Some(deferred);
                };
                ui.end_row();
            });
    }
}

impl App {
    pub(super) fn about(&mut self, ui: &mut egui::Ui) {
        egui::Grid::new("about").num_columns(2).spacing([16.0, 8.0]).show(ui, |ui| {
            ui.label("版本");
            ui.label(format!("microchat {}", env!("CARGO_PKG_VERSION")));
            ui.end_row();

            ui.label("后端");
            ui.label(RichText::new(&self.settings.server_address).monospace());
            ui.end_row();

            ui.label("连接");
            ui.label(match &self.backend {
                Backend::Online => "已连接",
                Backend::Checking => "连接中",
                Backend::Offline(_) => "未连接",
            });
            ui.end_row();

            ui.label("前端设置");
            ui.label(RichText::new(FrontendSettings::path().display().to_string()).monospace());
            ui.end_row();

            ui.label("域名");
            ui.label(RichText::new("会话仍为本地占位，尚未接后端存储").weak());
            ui.end_row();
        });
    }
}
