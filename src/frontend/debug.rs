//! debug：从原来的 main.rs 拆出来的（纯搬家，零行为变更）。

use super::*;

impl App {
    pub(super) fn fetch_debug(&mut self) {
        if self.debug_file_name.is_empty() {
            self.debug_file_name = "config.json".to_owned();
        }
        let (base, token) = (self.settings.server_address.clone(), self.token.clone());
        self.client.send(Command::DebugState { base, token });
        self.fetch_debug_file();
        self.fetch_debug_payload();
        self.fetch_graph();
    }
}

impl App {
    pub(super) fn fetch_debug_file(&mut self) {
        let (base, token) = (self.settings.server_address.clone(), self.token.clone());
        let name = self.debug_file_name.clone();
        self.client.send(Command::DebugFile { base, token, name });
    }
}

impl App {
    /// 拉"最近一次发给上游的载荷"（调试用，只活在内存里）。
    pub(super) fn fetch_debug_payload(&mut self) {
        let (base, token) = (self.settings.server_address.clone(), self.token.clone());
        self.client.send(Command::DebugPayload { base, token });
    }
}

impl App {
    pub(super) fn fetch_debug_state(&mut self) {
        let (base, token) = (self.settings.server_address.clone(), self.token.clone());
        self.client.send(Command::DebugState { base, token });
    }
}

impl App {
    /// 调试页：与设置页同构——左导航 + 分页，别把工具堆成一长条。
    /// 关系图独立成页还顺手解决滚轮语义之争（图里滚轮＝缩放，不再和页面滚动抢）。
    pub(super) fn debug_view(&mut self, ui: &mut egui::Ui) {
        ui.horizontal(|ui| {
            ui.heading("调试");
            ui.with_layout(Layout::right_to_left(Align::Center), |ui| {
                let refresh = match self.debug_tab {
                    DebugTab::State => Some("刷新状态"),
                    DebugTab::Payload => Some("重新拉取"),
                    DebugTab::Config => Some("重新读取"),
                    DebugTab::Log => None,
                    DebugTab::Graph => Some("重新拉取"),
                };
                if let Some(label) = refresh {
                    if ui.button(label).clicked() {
                        match self.debug_tab {
                            DebugTab::State => self.fetch_debug_state(),
                            DebugTab::Payload => self.fetch_debug_payload(),
                            DebugTab::Config => self.fetch_debug_file(),
                            DebugTab::Graph => self.fetch_graph(),
                            DebugTab::Log => {}
                        }
                    }
                }
            });
        });
        ui.separator();
        if !self.note.is_empty() {
            ui.label(RichText::new(&self.note).weak());
        }

        egui::Panel::left("debug_nav")
            .resizable(false)
            .default_size(150.0)
            .show(ui, |ui| {
                ui.add_space(6.0);
                for (tab, label) in DebugTab::ALL {
                    ui.selectable_value(&mut self.debug_tab, tab, label);
                }
            });

        match self.debug_tab {
            DebugTab::State => self.debug_state_tab(ui),
            DebugTab::Payload => self.debug_payload_tab(ui),
            DebugTab::Config => self.debug_config_tab(ui),
            DebugTab::Log => self.debug_log_tab(ui),
            DebugTab::Graph => self.debug_graph_tab(ui),
        }
    }
}

impl App {
    pub(super) fn debug_state_tab(&mut self, ui: &mut egui::Ui) {
        egui::ScrollArea::vertical()
            .id_salt("debug_state_scroll")
            .auto_shrink([false, false])
            .show(ui, |ui| match &self.debug_state {
                None => {
                    ui.label(RichText::new("加载中…（后端若刚重启，点右上角「刷新状态」）").weak());
                }
                Some(state) => {
                    let rows: [(&str, String); 9] = [
                        ("版本", state.version.clone()),
                        (
                            "鉴权",
                            if state.auth_enabled {
                                "已启用"
                            } else {
                                "未启用"
                            }
                            .to_owned(),
                        ),
                        ("配置目录", state.config_dir.clone()),
                        ("数据目录", state.data_dir.clone()),
                        ("数据库", state.db_path.clone()),
                        (
                            "会话 / 消息 / 模型",
                            format!(
                                "{} / {} / {}",
                                state.counts.conversations,
                                state.counts.messages,
                                state.counts.models
                            ),
                        ),
                        ("标题字数", state.chat_title_chars.to_string()),
                        (
                            "默认 provider / model / agent",
                            format!(
                                "{} / {} / {}",
                                state.default_provider, state.default_model, state.default_agent
                            ),
                        ),
                        (
                            "已配置 providers / agents",
                            format!("{} / {}", state.providers_configured, state.agents_configured),
                        ),
                    ];
                    egui::Grid::new("debug_state")
                        .num_columns(2)
                        .spacing([16.0, 6.0])
                        .show(ui, |ui| {
                            for (key, value) in rows {
                                ui.label(key);
                                ui.label(RichText::new(value).monospace());
                                ui.end_row();
                            }
                        });
                }
            });
    }
}

impl App {
    pub(super) fn debug_config_tab(&mut self, ui: &mut egui::Ui) {
        ui.label(
            RichText::new("只读白名单：config / providers / agents；密钥文件永不出现在这里").weak(),
        );
        let mut pick: Option<&'static str> = None;
        ui.horizontal(|ui| {
            for name in ["config.json", "providers.json", "agents.json"] {
                if ui
                    .selectable_label(self.debug_file_name == name, name)
                    .clicked()
                {
                    pick = Some(name);
                }
            }
        });
        if let Some(name) = pick {
            self.debug_file_name = name.to_owned();
            self.fetch_debug_file();
        }

        match &self.debug_file {
            None => {
                ui.label(RichText::new("选一个文件查看").weak());
            }
            Some(file) => {
                ui.label(
                    RichText::new(format!(
                        "{}  ·  {} 字符",
                        file.name,
                        file.text.chars().count()
                    ))
                    .weak(),
                );
                egui::ScrollArea::vertical()
                    .id_salt("debug_raw_file")
                    .auto_shrink([false, false])
                    .show(ui, |ui| {
                        ui.add(egui::Label::new(RichText::new(&file.text).monospace()).wrap());
                    });
            }
        }
    }
}

impl App {
    /// 请求日志纯本地，不需要刷新按钮。
    pub(super) fn debug_log_tab(&mut self, ui: &mut egui::Ui) {
        let log = self.client.log_snapshot();
        if log.is_empty() {
            ui.label(RichText::new("（暂无）").weak());
            return;
        }
        egui::ScrollArea::vertical()
            .id_salt("debug_log")
            .auto_shrink([false, false])
            .stick_to_bottom(true)
            .show(ui, |ui| {
                for line in log {
                    ui.monospace(line);
                }
            });
    }
}

impl App {
    /// 「最近发送载荷」：原样看**最近一次实际发给上游**的 `/chat/completions` 请求体。
    ///
    /// 与「会话里的 `/outgoing`」不是一回事：那个是"**预计**会发什么"（从库里重拼一遍），
    /// 这个是"**实际**发了什么"（线上的形状：`role` 取值、`stream` 开关、整个 JSON）——
    /// 排查"看着都对、上游却报错"就看它。
    pub(super) fn debug_payload_tab(&mut self, ui: &mut egui::Ui) {
        ui.label(
            RichText::new("最近一次实际发给上游的 /chat/completions 请求体（只在后端内存里，重启即失）")
                .weak(),
        );
        ui.add_space(4.0);
        let Some(payload) = self.debug_payload.clone() else {
            ui.label(RichText::new("还没拉到").weak());
            return;
        };
        if payload.is_null() {
            ui.label(
                RichText::new(
                    "后端还没往上游发过东西。（`dummy` 与兜底话术不算——它们压根不出网。）\n\
                     去会话里用真模型发一条，再点「重新拉取」。",
                )
                .weak(),
            );
            return;
        }
        let provider = payload["provider"].as_str().unwrap_or("");
        let model = payload["model"].as_str().unwrap_or("");
        let at_ms = payload["at_ms"].as_i64().unwrap_or(0);
        let body = payload.get("body").cloned().unwrap_or(serde_json::Value::Null);
        let text = serde_json::to_string_pretty(&body).unwrap_or_default();
        let now = std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .map(|d| d.as_millis() as i64)
            .unwrap_or(0);
        let age = (now.saturating_sub(at_ms)) as f64 / 1000.0;
        let when = if age < 90.0 {
            format!("{age:.0} 秒前")
        } else {
            format!("{:.1} 分钟前", age / 60.0)
        };
        ui.horizontal(|ui| {
            ui.label(RichText::new(format!("{provider} / {model}")).strong());
            ui.label(RichText::new(format!("· {when} · {} 字节", text.len())).weak());
            ui.with_layout(Layout::right_to_left(Align::Center), |ui| {
                if ui.button("复制").clicked() {
                    ui.output_mut(|out| {
                        out.commands
                            .push(egui::OutputCommand::CopyText(text.clone()))
                    });
                }
            });
        });
        ui.separator();
        egui::ScrollArea::both()
            .id_salt("debug_payload")
            .auto_shrink([false, false])
            .show(ui, |ui| {
                let mut readonly = text.as_str();
                ui.add(
                    TextEdit::multiline(&mut readonly)
                        .frame(egui::Frame::NONE)
                        .desired_width(f32::INFINITY)
                        .code_editor(),
                );
            });
    }
}
