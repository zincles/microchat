//! 任务：一屏看全后台在跑什么（生成 / 压缩 / 刷新模型……）。
//!
//! 数据来自 `GET /tasks`（后端 `crate::task::TaskRegistry`，进程内的事实）。
//! 左栏那行指示器也读同一份 —— 一处取数、两处显示。

use super::*;

impl App {
    /// 「任务」页：正在跑的在最前，下面是最近结束的。
    pub(super) fn tasks_view(&mut self, ui: &mut egui::Ui) {
        ui.heading("任务");
        ui.label(
            RichText::new(
                "进程内的事实（后端一重启就清零）——生成、压缩、刷新模型都在这儿挂号。\
                 流的细节（流到第几个字、取消）不在这张表里，它归那一刻的会话状态。",
            )
            .weak()
            .small(),
        );
        ui.add_space(8.0);

        let Some(board) = self.task_board.clone() else {
            ui.label(RichText::new("加载中…").weak());
            return;
        };

        ui.horizontal(|ui| {
            if board.running == 0 {
                ui.label(RichText::new("此刻空闲").weak());
            } else {
                ui.label(
                    RichText::new(format!("正在跑 {} 个", board.running))
                        .strong()
                        .color(egui::Color32::from_rgb(120, 170, 120)),
                );
            }
            let finished = board.tasks.len().saturating_sub(board.running);
            ui.label(
                RichText::new(format!("· 最近 {finished} 条记录"))
                    .weak()
                    .small(),
            );
        });
        ui.add_space(6.0);

        egui::ScrollArea::vertical().show(ui, |ui| {
            egui::Grid::new("tasks_grid")
                .num_columns(5)
                .spacing([14.0, 5.0])
                .striped(true)
                .show(ui, |ui| {
                    for header in ["状态", "类型", "在做什么", "跑了", "结果"] {
                        ui.label(RichText::new(header).weak().small());
                    }
                    ui.end_row();

                    for task in &board.tasks {
                        if task.running() {
                            ui.label(
                                RichText::new("跑着")
                                    .color(egui::Color32::from_rgb(120, 170, 120))
                                    .small(),
                            );
                        } else {
                            ui.label(RichText::new("结束").weak().small());
                        }
                        ui.label(RichText::new(task.kind.label()).small());
                        ui.label(RichText::new(&task.title).small());
                        ui.label(RichText::new(fmt_duration(task.elapsed_ms(board.now))).weak().small());
                        let outcome = task.outcome.as_deref().unwrap_or("—");
                        // 失败/中断的字样要能一眼扫到：颜色按内容分，而不是按谁写的
                        let color = if outcome.starts_with("失败") || outcome.starts_with("中断") {
                            egui::Color32::from_rgb(210, 130, 130)
                        } else {
                            ui.visuals().weak_text_color()
                        };
                        ui.label(RichText::new(outcome).color(color).small());
                        ui.end_row();
                    }
                });
        });
    }
}
