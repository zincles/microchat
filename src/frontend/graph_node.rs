//! 关系图里「拼图块」式的节点渲染。
//!
//! egui_graphs 的显示层是可插拔的：`DisplayNode::shapes` 返回任意 `Shape`，坐标用
//! `DrawContext::meta` 从画布坐标投影到屏幕。所以引擎（分层布局、缩放、平移、拖拽）
//! 照用，只把"一个圆点"换成"一块拼图"——Scratch 那种观感，才配得上消息序列。
//!
//! 块里只放**角色徽章 + 摘要**：聊天消息是长文本，真把全文塞进块里，块就会变成一堵墙
//! （Scratch 的块之所以好看，是因为它们矮而可组合）。

use eframe::egui::{
    epaint::{RectShape, TextShape},
    Color32, CornerRadius, FontFamily, FontId, Pos2, Rect, Shape, Stroke, StrokeKind, Vec2,
};
use egui_graphs::{DisplayNode, DrawContext, NodeProps};
use petgraph::stable_graph::IndexType;
use petgraph::EdgeType;

/// 块高与每字符宽度（画布坐标，缩放由 `meta` 处理）。
const BLOCK_HEIGHT: f32 = 28.0;
const CHAR_WIDTH: f32 = 13.0;
const BLOCK_MIN_WIDTH: f32 = 96.0;
const BLOCK_MAX_WIDTH: f32 = 420.0;
const PADDING: f32 = 12.0;
const CORNER: u8 = 6;
/// 字号（画布坐标）。
const FONT_SIZE: f32 = 13.0;

#[derive(Clone, Debug)]
pub struct BlockNodeShape {
    pos: Pos2,
    selected: bool,
    dragged: bool,
    hovered: bool,
    color: Option<Color32>,
    label: String,
}

impl<N: Clone> From<NodeProps<N>> for BlockNodeShape {
    fn from(props: NodeProps<N>) -> Self {
        Self {
            pos: props.location(),
            selected: props.selected,
            dragged: props.dragged,
            hovered: props.hovered,
            color: props.color(),
            label: props.label.clone(),
        }
    }
}

impl<N: Clone, E: Clone, Ty: EdgeType, Ix: IndexType> DisplayNode<N, E, Ty, Ix>
    for BlockNodeShape
{
    fn is_inside(&self, pos: Pos2) -> bool {
        block_rect(self.pos, &self.label).contains(pos)
    }

    /// 边吸附到**矩形边界**上（默认实现是吸附到圆上）。
    fn closest_boundary_point(&self, dir: Vec2) -> Pos2 {
        rect_boundary_point(self.pos, block_size(&self.label), dir)
    }

    fn shapes(&mut self, ctx: &DrawContext) -> Vec<Shape> {
        let zoom = ctx.meta.zoom;
        let size = block_size(&self.label) * zoom;
        let center = ctx.meta.canvas_to_screen_pos(self.pos);
        let rect = Rect::from_center_size(center, size);
        let fill = self.color.unwrap_or_else(|| {
            ctx.ctx
                .global_style()
                .visuals
                .widgets
                .inactive
                .fg_stroke
                .color
        });
        let stroke = ctx.style.resolve_node_stroke(
            self.selected,
            self.dragged,
            self.color,
            Stroke::default(),
            &ctx.ctx.global_style(),
        );
        let radius = CornerRadius::same(CORNER);

        let mut shapes = Vec::with_capacity(3);

        // 块身
        shapes.push(RectShape::new(rect, radius, fill, stroke, StrokeKind::Inside).into());

        // 底部小凸起：拼图块的连接头（图形上没有布尔运算，用一块小矩形凑出剪影）
        let stub = Rect::from_center_size(
            Pos2::new(rect.center().x, rect.max.y),
            Vec2::new(20.0 * zoom, 8.0 * zoom),
        );
        shapes.push(RectShape::new(stub, CornerRadius::same(3), fill, Stroke::NONE, StrokeKind::Inside).into());

        // 文字：字号随缩放走（和默认渲染一致），块内居中
        let text_color = readable_on(fill);
        let galley = ctx.ctx.fonts_mut(|fonts| {
            fonts.layout_no_wrap(
                self.label.clone(),
                FontId::new(FONT_SIZE * zoom, FontFamily::Proportional),
                text_color,
            )
        });
        let text_pos = Pos2::new(
            rect.center().x - galley.size().x / 2.0,
            rect.center().y - galley.size().y / 2.0,
        );
        shapes.push(TextShape::new(text_pos, galley, text_color).into());

        shapes
    }

    fn update(&mut self, state: &NodeProps<N>) {
        self.pos = state.location();
        self.selected = state.selected;
        self.dragged = state.dragged;
        self.hovered = state.hovered;
        self.color = state.color();
        self.label = state.label.clone();
    }
}

/// 块尺寸：宽度随文字长度长，高度固定。
pub fn block_size(label: &str) -> Vec2 {
    let width = (label.chars().count() as f32 * CHAR_WIDTH + PADDING * 2.0)
        .clamp(BLOCK_MIN_WIDTH, BLOCK_MAX_WIDTH);
    Vec2::new(width, BLOCK_HEIGHT)
}

fn block_rect(center: Pos2, label: &str) -> Rect {
    Rect::from_center_size(center, block_size(label))
}

/// 从矩形中心朝 `dir` 走，撞到边界的位置（把方向向量按最近的边裁一刀）。
fn rect_boundary_point(center: Pos2, size: Vec2, dir: Vec2) -> Pos2 {
    let half = size / 2.0;
    let (dx, dy) = (dir.x.abs(), dir.y.abs());
    if dx < 1e-6 && dy < 1e-6 {
        return center;
    }
    let tx = if dx > 1e-6 { half.x / dx } else { f32::INFINITY };
    let ty = if dy > 1e-6 { half.y / dy } else { f32::INFINITY };
    center + dir * tx.min(ty)
}

/// 在给定底色上挑一个能看清的字色（粗略亮度判断，够用）。
fn readable_on(fill: Color32) -> Color32 {
    let luma = 0.299 * fill.r() as f32 + 0.587 * fill.g() as f32 + 0.114 * fill.b() as f32;
    if luma > 150.0 {
        Color32::from_rgb(0x18, 0x18, 0x18)
    } else {
        Color32::WHITE
    }
}

#[cfg(test)]
mod tests {
    use eframe::egui::{Pos2, Vec2};

    use super::{block_size, readable_on, rect_boundary_point};

    #[test]
    fn boundary_point_snaps_to_rect_edges() {
        let center = Pos2::new(0.0, 0.0);
        let size = Vec2::new(100.0, 20.0);

        // 水平方向 → 撞左右边（x = ±50）
        let p = rect_boundary_point(center, size, Vec2::new(1.0, 0.0));
        assert!((p.x - 50.0).abs() < 1e-3 && p.y.abs() < 1e-3, "{p:?}");

        // 竖直方向 → 撞上下边（y = ±10）
        let p = rect_boundary_point(center, size, Vec2::new(0.0, 1.0));
        assert!(p.x.abs() < 1e-3 && (p.y - 10.0).abs() < 1e-3, "{p:?}");

        // 零向量 → 原地，别产生 NaN
        assert_eq!(rect_boundary_point(center, size, Vec2::ZERO), center);
    }

    #[test]
    fn block_width_follows_label_length() {
        let short = block_size("你");
        let long = block_size("一二三四五六七八九十");
        assert!(long.x > short.x, "长标签要更宽的块");
        assert_eq!(short.y, long.y, "高度固定");

        // 超长文本被钳住，别让块无限宽
        let huge = block_size(&"字".repeat(200));
        assert!(huge.x <= 420.0);
    }

    #[test]
    fn text_color_contrasts_with_fill() {
        // 浅色底用深字，深色底用白字
        assert_eq!(readable_on(eframe::egui::Color32::WHITE).r(), 0x18);
        assert_eq!(readable_on(eframe::egui::Color32::BLACK).r(), 0xFF);
    }
}
