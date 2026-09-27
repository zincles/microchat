//! graph：从原来的 main.rs 拆出来的（纯搬家，零行为变更）。

use super::*;

pub(super) const GRAPH_ID: &str = "debug_graph";

/// 同一列里相邻块之间的垂直间隔；会话（列）之间的水平间隔（画布坐标）。
pub(super) const STACK_GAP: f32 = 26.0;

pub(super) const COLUMN_GAP: f32 = 48.0;

/// 摘要道：挂在列右侧多远处（每上一层再往外一档）。
pub(super) const SUMMARY_LANE_GAP: f32 = 150.0;

/// 滚轮换算成"点"的比例：一行 50 点、一页 400 点（不同后端给的单位不一样）。
pub(super) const POINTS_PER_LINE: f32 = 50.0;

pub(super) const POINTS_PER_PAGE: f32 = 400.0;

/// 关系图里的节点类型——颜色按它区分（与聊天界面里「你/助手」的配色一致）。
#[derive(Clone, Copy, PartialEq)]
pub(super) enum GraphNodeKind {
    /// 会话：一棵树的根
    Conversation,
    /// 系统提示词（第一句）
    System,
    User,
    Assistant,
    /// 摘要（compaction 的产出）：**不在链上**，挂在它覆盖的那一段右边
    Summary,
}

impl GraphNodeKind {
    fn color(self) -> Color32 {
        match self {
            Self::Conversation => Color32::from_rgb(0xe0, 0x9f, 0x2f),
            Self::System => Color32::from_rgb(0x8a, 0x8a, 0x8a),
            Self::User => Color32::from_rgb(0x2f, 0x6f, 0xe0),
            Self::Assistant => Color32::from_rgb(0x2f, 0x9e, 0x44),
            Self::Summary => Color32::from_rgb(0x9a, 0x5f, 0xd0),
        }
    }
}

impl App {
    pub(super) fn fetch_graph(&mut self) {
        let (base, token) = (self.settings.server_address.clone(), self.token.clone());
        for conversation in self.conversations.iter().take(GRAPH_CONVERSATION_LIMIT) {
            self.client.send(Command::ListMessages {
                base: base.clone(),
                token: token.clone(),
                conversation: conversation.id,
            });
        }
        self.client.send(Command::ListConversations { base, token });
    }
}

impl App {
    /// 把后端数据投影成关系图：每个会话是一棵有向树——
    /// 会话（根）→ 系统提示词 → 用户/助手交替的消息链。
    /// `messages.parent_id` 还没落地（Phase 3），所以链还是线性的；那次迁移之后
    /// 只需把"上一条→这条"换成"父→子"，分叉（重新生成/分支）会自然长出来。
    pub(super) fn rebuild_graph(&mut self) {
        let mut graph = BlockGraph::new();
        let mut column_left = 0.0_f32;
        let mut max_height = 1.0_f32;

        for conversation in &self.conversations {
            // 助手节点写 agent 的名字（和会话里的消息对齐）
            let agent_name = self.agent_label(&conversation.agent_id);
            // 一列的块：会话（根）→ 系统提示词 → 用户/助手交替，**自上而下**。
            // 第三项 = "这条块对应哪条消息"（摘要要拿它连边）；系统/会话块是 None。
            let mut nodes: Vec<(GraphNodeKind, String, Option<Uuid>)> = Vec::new();
            let title = if conversation.title.is_empty() {
                format!("会话 {}", &conversation.id.to_string()[..8])
            } else {
                conversation.title.clone()
            };
            nodes.push((GraphNodeKind::Conversation, title, None));

            if let Some(prompt) = self.effective_system_prompt(conversation) {
                let excerpt: String = prompt.chars().take(16).collect();
                nodes.push((GraphNodeKind::System, format!("system · {excerpt}"), None));
            }

            for (index, message) in self
                .messages
                .get(&conversation.id)
                .into_iter()
                .flatten()
                .enumerate()
            {
                let (kind, who) = match message.role {
                    ApiRole::User => (GraphNodeKind::User, "用户"),
                    ApiRole::Assistant => (GraphNodeKind::Assistant, agent_name.as_str()),
                };
                let excerpt: String = message.content.chars().take(12).collect();
                nodes.push((
                    kind,
                    format!("{} {}·{}", index + 1, who, excerpt),
                    Some(message.id),
                ));
            }

            // 列内：块自上而下摞起来；列本身宽度取该列最宽的块，所有块共用一条中心轴
            let sizes: Vec<egui::Vec2> = nodes
                .iter()
                .map(|(_, label, _)| graph_node::block_size(label))
                .collect();
            let width = column_width(&sizes);
            let center_x = column_left + width / 2.0;
            let heights: Vec<f32> = sizes.iter().map(|size| size.y).collect();
            let centers_y = stacked_centers(&heights, STACK_GAP);

            let mut message_nodes: BTreeMap<Uuid, petgraph::graph::NodeIndex> = BTreeMap::new();
            let mut message_y: BTreeMap<Uuid, f32> = BTreeMap::new();
            let mut previous = None;
            for ((kind, label, message_id), y) in nodes.into_iter().zip(centers_y.iter().copied()) {
                let node = graph.add_node_custom((), |node| {
                    node.set_label(label);
                    node.set_color(kind.color());
                    node.set_location(egui::pos2(center_x, y));
                });
                if let Some(previous) = previous {
                    graph.add_edge(previous, node, ());
                }
                if let Some(message_id) = message_id {
                    message_nodes.insert(message_id, node);
                    message_y.insert(message_id, y);
                }
                previous = Some(node);
            }

            let last_half = sizes.last().map_or(0.0, |size| size.y / 2.0);
            let column_height = centers_y.last().copied().unwrap_or(0.0) + last_half;

            // ── 摘要：**链不动**，挂在它覆盖的那一段右边（金字塔一眼可见）──
            //
            // 边：被它覆盖的每条消息 → 它；再往上一层，子摘要 → 父摘要。
            // 层级 = 沿 parent_summary_id 往上数的深度 ⇒ 越上层越靠右。
            let summaries = self
                .summaries
                .get(&conversation.id)
                .cloned()
                .unwrap_or_default();
            let members_of: BTreeMap<Uuid, Vec<Uuid>> = {
                let mut map: BTreeMap<Uuid, Vec<Uuid>> = BTreeMap::new();
                for message in self.messages.get(&conversation.id).into_iter().flatten() {
                    if let Some(summary_id) = message.summary_id {
                        map.entry(summary_id).or_default().push(message.id);
                    }
                }
                map
            };
            let by_id: BTreeMap<Uuid, usize> = summaries
                .iter()
                .enumerate()
                .map(|(index, summary)| (summary.id, index))
                .collect();
            // 先算层级（沿 parent 往上走），再按层级升序摆 —— 孩子先摆，父才找得到它们
            let mut ordered: Vec<(usize, &microchat::registry::SummaryView)> = summaries
                .iter()
                .map(|summary| {
                    let mut level = 1;
                    let mut cursor = summary.parent_summary_id;
                    while let Some(id) = cursor {
                        level += 1;
                        cursor = by_id
                            .get(&id)
                            .map(|index| summaries[*index].parent_summary_id)
                            .flatten();
                    }
                    (level, summary)
                })
                .collect();
            ordered.sort_by_key(|(level, _)| *level);

            let mut summary_nodes: BTreeMap<Uuid, petgraph::graph::NodeIndex> = BTreeMap::new();
            let mut summary_y: BTreeMap<Uuid, f32> = BTreeMap::new();
            let mut widest_lane = 0.0_f32;
            let mut max_level = 1;
            for (level, summary) in &ordered {
                max_level = max_level.max(*level);
                // 纵向：覆盖消息 ⇒ 取那一段的中点；摘要的摘要 ⇒ 取孩子的中点
                let y = if let (Some(first), Some(last)) =
                    (summary.first_message_id, summary.last_message_id)
                {
                    match (message_y.get(&first), message_y.get(&last)) {
                        (Some(first), Some(last)) => (first + last) / 2.0,
                        _ => column_height - 8.0,
                    }
                } else {
                    let children: Vec<f32> = summaries
                        .iter()
                        .filter(|child| child.parent_summary_id == Some(summary.id))
                        .filter_map(|child| summary_y.get(&child.id).copied())
                        .collect();
                    if children.is_empty() {
                        // 孤立摘要（成员都被剪/删了）：摆在列的尾巴附近，等你去清理
                        column_height + 18.0
                    } else {
                        children.iter().sum::<f32>() / children.len() as f32
                    }
                };
                let excerpt: String = summary.text.chars().take(12).collect();
                let dirty_mark = if summary.dirty { "（已过期）" } else { "" };
                let label = format!(
                    "摘要 {}块·{}tok · {excerpt}{dirty_mark}",
                    summary.blocks, summary.tokens
                );
                let size = graph_node::block_size(&label);
                widest_lane = widest_lane.max(size.x);
                let x = center_x + width / 2.0 + SUMMARY_LANE_GAP * (*level as f32);
                let color = if summary.dirty {
                    Color32::from_rgb(0xd0, 0x8f, 0x2f)
                } else {
                    GraphNodeKind::Summary.color()
                };
                let node = graph.add_node_custom((), |node| {
                    node.set_label(label);
                    node.set_color(color);
                    node.set_location(egui::pos2(x, y));
                });
                summary_y.insert(summary.id, y);

                for member in members_of.get(&summary.id).into_iter().flatten() {
                    if let Some(child) = message_nodes.get(member) {
                        graph.add_edge(*child, node, ());
                    }
                }
                for child in summaries
                    .iter()
                    .filter(|child| child.parent_summary_id == Some(summary.id))
                {
                    if let Some(child_node) = summary_nodes.get(&child.id) {
                        graph.add_edge(*child_node, node, ());
                    }
                }
                summary_nodes.insert(summary.id, node);
            }

            max_height = max_height.max(column_height + if summaries.is_empty() { 0.0 } else { 40.0 });
            // 这一列的横向占地 = 本列 + 右侧摘要道（别让下一列压上来）
            let lane = if summaries.is_empty() {
                0.0
            } else {
                SUMMARY_LANE_GAP * (max_level as f32 + 1.0) + widest_lane
            };
            column_left += width + COLUMN_GAP + lane;
        }

        self.graph_bounds = egui::Rect::from_min_max(
            egui::pos2(0.0, 0.0),
            egui::pos2((column_left - COLUMN_GAP).max(1.0), max_height),
        );
        self.graph_needs_fit = true;
        self.graph = Some(graph);
    }
}

impl App {
    /// 关系图占满整页：滚轮在这里只可能是缩放。
    pub(super) fn debug_graph_tab(&mut self, ui: &mut egui::Ui) {
        let mut want_fit = false;
        ui.horizontal(|ui| {
            if ui
                .add_enabled(self.graph.is_some(), egui::Button::new("重新布局"))
                .on_hover_text("按会话重新排列块的位置")
                .clicked()
            {
                self.rebuild_graph();
                want_fit = true;
            }
            if ui
                .add_enabled(self.graph.is_some(), egui::Button::new("适配视图"))
                .clicked()
            {
                want_fit = true;
            }
            ui.separator();
            ui.label(RichText::new("滚轮缩放 · 拖空白平移 · 拖块移动").weak());
        });
        ui.horizontal(|ui| {
            for (kind, label) in [
                (GraphNodeKind::Conversation, "会话"),
                (GraphNodeKind::System, "系统"),
                (GraphNodeKind::User, "用户"),
                (GraphNodeKind::Assistant, "助手"),
            ] {
                let (rect, _) = ui.allocate_exact_size(egui::vec2(10.0, 10.0), egui::Sense::hover());
                ui.painter().add(egui::Shape::rect_filled(
                    rect,
                    egui::CornerRadius::same(2),
                    kind.color(),
                ));
                ui.label(RichText::new(label).weak());
            }
            ui.label(RichText::new("（每个会话一列，自上而下＝消息顺序；分支＝重新生成/分支，待消息父指针落地）").weak());
        });

        let Some(graph) = &mut self.graph else {
            ui.label(RichText::new("点右上角「重新拉取」载入").weak());
            return;
        };

        let interactions = egui_graphs::SettingsInteraction::new()
            .with_dragging_enabled(true)
            .with_node_selection_enabled(true);
        let navigation = egui_graphs::SettingsNavigation::new()
            .with_zoom_and_pan_enabled(true)
            // 每帧 fit 会跟滚轮缩放、拖动打架，所以关掉——重建后自己适配一次
            .with_fit_to_screen_enabled(false);

        let mut viewport = None;
        ui.allocate_ui(ui.available_size(), |ui| {
            let response = BlockGraphView::new()
                .with_interactions(&interactions)
                .with_navigations(&navigation)
                .with_id(Some(GRAPH_ID.to_owned()))
                .show(ui, graph)
                .response;
            wheel_zoom(ui, &response, GRAPH_ID);
            viewport = Some(response.rect);
        });

        if let Some(viewport) = viewport {
            if want_fit || self.graph_needs_fit {
                fit_view(ui, viewport, GRAPH_ID, self.graph_bounds);
                self.graph_needs_fit = false;
            }
        }
    }
}

/// 普通滚轮也缩放。
///
/// egui_graphs 只认 egui 的 `zoom_delta()`——那等于 **Ctrl+滚轮 / 触控板捏合**，
/// 普通滚轮被它直接忽略（用户会以为"不能缩放"）。这里按它自己的坐标公式补一刀：
/// `screen = canvas * zoom + pan`，所以要保持指针下的画布点不动：
/// `pan' = pointer - (pointer - pan) * (zoom'/zoom)`。
///
/// 取的是**原始**滚轮事件，不是 `smooth_scroll_delta`：后者是 egui 逐帧平滑过的值，
/// 一次滚轮会连续改好几帧状态（观感像缩放动画，也白烧性能）。原始事件一帧就是一次。
pub(super) fn wheel_zoom(ui: &mut egui::Ui, response: &egui::Response, id: &str) {
    let Some(pointer) = response.hover_pos() else {
        return;
    };
    let points = ui.input(|i| {
        i.raw
            .events
            .iter()
            .filter_map(|event| match event {
                egui::Event::MouseWheel { unit, delta, .. } => Some(match unit {
                    egui::MouseWheelUnit::Line => delta.y * POINTS_PER_LINE,
                    egui::MouseWheelUnit::Point => delta.y,
                    egui::MouseWheelUnit::Page => delta.y * POINTS_PER_PAGE,
                }),
                _ => None,
            })
            .sum::<f32>()
    });
    if points == 0.0 {
        return;
    }

    let mut meta = egui_graphs::MetadataFrame::new(Some(id.to_owned())).load(ui);
    let old_zoom = meta.zoom;
    let new_zoom = (old_zoom * (1.0 + points * 0.002)).clamp(0.05, 4.0);
    if new_zoom == old_zoom {
        return;
    }

    let pointer = pointer.to_vec2();
    let canvas_point = (pointer - meta.pan) / old_zoom;
    meta.pan = pointer - canvas_point * new_zoom;
    meta.zoom = new_zoom;
    meta.save(ui);
}

/// 把整张图放进视口。
///
/// 位置是我们自己排的，所以包围盒也知道——不必依赖 crate 的每帧 fit
/// （那个会在用户拖块/滚轮缩放时不断抢回去）。
pub(super) fn fit_view(ui: &mut egui::Ui, viewport: egui::Rect, id: &str, bounds: egui::Rect) {
    if !bounds.width().is_finite() || !bounds.height().is_finite() {
        return;
    }
    if bounds.width() <= 0.0 || bounds.height() <= 0.0 || !viewport.width().is_finite() {
        return;
    }

    let padding = 32.0;
    let zoom = ((viewport.width() - padding) / bounds.width())
        .min((viewport.height() - padding) / bounds.height())
        .clamp(0.05, 4.0);

    let mut meta = egui_graphs::MetadataFrame::new(Some(id.to_owned())).load(ui);
    meta.zoom = zoom;
    meta.pan = viewport.center().to_vec2() - bounds.center().to_vec2() * zoom;
    meta.save(ui);
}

/// 把一串「尺寸 + 间隔」折成一维的中心坐标。
///
/// 一列里的块自上而下摞起来（块高固定，于是是等差数列）、各列左右并排（列宽取该列最宽的块），
/// 都用它。用块自己的尺寸推进，所以宽高不一的块**绝不重叠**——之前"全挤在一起"
/// 正是因为布局引擎按固定间距摆，而这里的块宽度从 90 到 420 不等。
pub(super) fn stacked_centers(sizes: &[f32], gap: f32) -> Vec<f32> {
    let mut cursor = 0.0;
    sizes
        .iter()
        .map(|size| {
            let center = cursor + size / 2.0;
            cursor += size + gap;
            center
        })
        .collect()
}

/// 一列的宽度：该列**最宽**的块（列内所有块共用一条中心轴；列宽不够，相邻列就会叠）。
pub(super) fn column_width(sizes: &[egui::Vec2]) -> f32 {
    sizes.iter().map(|size| size.x).fold(0.0, f32::max)
}
