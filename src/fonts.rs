//! CJK 字体装载。
//!
//! epaint 0.36 不做系统字体回退，默认字体集也不含 CJK——缺字直接渲染成豆腐块。
//! 所以启动时挑一个可用的系统 CJK 字体，追加到 Proportional / Monospace 的回退链尾部
//! （默认拉丁字体仍在前，中西文混排不塌）。
//!
//! 强制指定：`MICROCHAT_FONT=<路径>` + `MICROCHAT_FONT_INDEX=<face>`。
//! `.ttc` 集合内各 face 的字形风格不同（Noto CJK 常见 index 0 为 JP），需要 SC 时用后者挑。

use std::path::{Path, PathBuf};
use std::sync::Arc;

use eframe::egui;

pub struct Loaded {
    pub path: PathBuf,
    pub index: u32,
}

pub fn install_cjk(ctx: &egui::Context) -> Result<Loaded, String> {
    let (path, index, bytes) = pick_font()?;

    let mut data = egui::FontData::from_owned(bytes);
    data.index = index;

    let mut defs = egui::FontDefinitions::default();
    defs.font_data.insert("cjk".to_owned(), Arc::new(data));
    for family in [egui::FontFamily::Proportional, egui::FontFamily::Monospace] {
        if let Some(list) = defs.families.get_mut(&family) {
            list.push("cjk".to_owned());
        }
    }
    ctx.set_fonts(defs);

    Ok(Loaded { path, index })
}

/// 返回 (路径, face index, 字节)，已通过 skrifa 解析校验（与 epaint 同一解析器）。
fn pick_font() -> Result<(PathBuf, u32, Vec<u8>), String> {
    if let Some(path) = std::env::var_os("MICROCHAT_FONT") {
        let index = std::env::var("MICROCHAT_FONT_INDEX")
            .ok()
            .and_then(|s| s.parse().ok())
            .unwrap_or(0);
        let path = PathBuf::from(path);
        return read_valid(&path, index)
            .map(|bytes| (path.clone(), index, bytes))
            .ok_or_else(|| format!("{} face {index} 解析失败", path.display()));
    }

    for path in scan_candidates() {
        if let Some(found) = read_valid(&path, 0) {
            return Ok((path, 0, found));
        }
    }
    Err("未找到可用 CJK 字体：装 fonts-noto-cjk，或设 MICROCHAT_FONT=<字体路径>".to_owned())
}

fn read_valid(path: &Path, index: u32) -> Option<Vec<u8>> {
    let bytes = std::fs::read(path).ok()?;
    skrifa::FontRef::from_index(&bytes, index).ok()?;
    Some(bytes)
}

const HINTS: &[(&str, i32)] = &[
    ("notosanscjk", 200),
    ("noto sans cjk", 200),
    ("notosanssc", 190),
    ("noto sans sc", 190),
    ("sarasa", 160),
    ("sourcehan", 150),
    ("source-han", 150),
    ("source_han", 150),
    ("wqy", 130),
    ("wenquanyi", 130),
    ("yahei", 130),
    ("msyh", 130),
    ("droidsansfallback", 120),
    ("uming", 100),
    ("ukai", 100),
    ("cjk", 90),
];

const PENALTY: &[(&str, i32)] = &[
    ("extralight", 60),
    ("semibold", 60),
    ("condensed", 60),
    ("italic", 80),
    ("oblique", 80),
    ("light", 50),
    ("thin", 50),
    ("black", 80),
    ("bold", 80),
    ("medium", 30),
];

fn score(path: &Path) -> i32 {
    let name = path.file_name().unwrap_or_default().to_string_lossy().to_lowercase();
    let mut s = HINTS
        .iter()
        .map(|(h, v)| if name.contains(h) { *v } else { 0 })
        .max()
        .unwrap_or(0);
    if s == 0 {
        return i32::MIN; // 无 CJK 线索的文件不参与
    }
    if name.contains("regular") {
        s += 40;
    }
    s -= PENALTY
        .iter()
        .map(|(p, v)| if name.contains(p) { *v } else { 0 })
        .max()
        .unwrap_or(0);
    s
}

fn scan_candidates() -> Vec<PathBuf> {
    let mut roots = vec![
        PathBuf::from("/usr/share/fonts"),
        PathBuf::from("/usr/local/share/fonts"),
    ];
    if let Some(home) = std::env::var_os("HOME") {
        let home = PathBuf::from(home);
        roots.push(home.join(".local/share/fonts"));
        roots.push(home.join(".fonts"));
    }

    let mut out = Vec::new();
    for root in &roots {
        walk(root, 0, &mut out);
    }
    out.retain(|p| score(p) > i32::MIN);
    out.sort_by(|a, b| score(b).cmp(&score(a)).then_with(|| a.cmp(b)));
    out
}

fn walk(dir: &Path, depth: u8, out: &mut Vec<PathBuf>) {
    if depth > 4 {
        return;
    }
    let Ok(entries) = std::fs::read_dir(dir) else {
        return;
    };
    for entry in entries.flatten() {
        let path = entry.path();
        if path.is_dir() {
            walk(&path, depth + 1, out);
        } else if is_font_file(&path) {
            out.push(path);
        }
    }
}

fn is_font_file(path: &Path) -> bool {
    matches!(
        path.extension().and_then(|e| e.to_str()).map(str::to_ascii_lowercase).as_deref(),
        Some("ttf" | "otf" | "ttc")
    )
}
