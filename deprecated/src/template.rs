//! 占位符：内置 Agent 的提示词里可以写的 `{{name}}`，在**发请求前的预处理**阶段被换掉。
//!
//! 三条硬边界（§26）：
//!
//! 1. **白名单就是枚举** —— 能用的名字写死在 [`Placeholder`] 里；不认识的 `{{foo}}`
//!    **原样留着**（不报错、也不猜），但会回报给调用方，界面上提示"这个变量不存在"。
//! 2. **只扫一遍** —— 替换进去的文本**不再当模板扫**。变量值里写 `{{...}}` 不生效，
//!    否则一段含 `{{` 的用户文本就能把注入玩坏。
//! 3. **只在内置 Agent 里生效** —— 会话 Agent 的提示词**永不**做替换：它一换，前缀缓存
//!    每轮全废（`world::build_outgoing` 里连这个词都不出现）。
//!
//! `prompt_version` 按**替换前**的模板正文算（见 `subagents::prompt_version`），
//! 否则版本会随内容每轮变，整批重做就失去了判据。

use std::collections::BTreeMap;

/// 可用的占位符。**想加一个就得改这里**（和内置 Agent 一样：名单写死在代码里）。
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Placeholder {
    /// 当前时间（UTC；值里带 `UTC` 字样，不含糊）。
    SystemTime,
    /// 这段材料**之前**的世界状态（逐键 `键 = 值`）。
    StateBefore,
    /// 这段材料**之后**的世界状态。
    StateAfter,
    /// 覆盖的条目区间，例如 `第 1–8 条`。
    Range,
    /// 覆盖了几个对话块。
    Blocks,
}

impl Placeholder {
    pub const ALL: [Placeholder; 5] = [
        Placeholder::SystemTime,
        Placeholder::StateBefore,
        Placeholder::StateAfter,
        Placeholder::Range,
        Placeholder::Blocks,
    ];

    /// 模板里写的名字（`{{system_time}}` 里的 `system_time`）。
    pub fn name(self) -> &'static str {
        match self {
            Self::SystemTime => "system_time",
            Self::StateBefore => "state_before",
            Self::StateAfter => "state_after",
            Self::Range => "range",
            Self::Blocks => "blocks",
        }
    }

    /// 一句话说明（界面上给用户看）。
    pub fn about(self) -> &'static str {
        match self {
            Self::SystemTime => "当前时间（UTC）",
            Self::StateBefore => "这段材料之前的世界状态（逐键 键 = 值）",
            Self::StateAfter => "这段材料之后的世界状态",
            Self::Range => "覆盖的条目区间，例如「第 1–8 条」",
            Self::Blocks => "覆盖了几个对话块",
        }
    }
}

/// 替换结果。
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Rendered {
    pub text: String,
    /// 模板里出现、但不在白名单里的名字（原样留在文本里）。
    pub unknown: Vec<String>,
}

/// 把 `{{name}}` 换成值。**只扫一遍**：`values` 里的内容原样进结果，不会再被当成模板。
///
/// - 没在 `values` 里给值的**已知变量**：留空串（调用方该给的没给，不该把 `{{...}}` 喂给模型）；
/// - **不认识**的 `{{foo}}`：原样留着，并记进 `unknown`；
/// - 没闭合的 `{{`：原样留着（它多半是正文里的普通文本）。
pub fn render(template: &str, values: &BTreeMap<&'static str, String>) -> Rendered {
    let mut text = String::with_capacity(template.len());
    let mut unknown = Vec::new();
    let bytes = template.as_bytes();
    let mut cursor = 0;

    while cursor < template.len() {
        let Some(open) = template[cursor..].find("{{") else {
            text.push_str(&template[cursor..]);
            break;
        };
        let open = cursor + open;
        text.push_str(&template[cursor..open]);
        let Some(close) = template[open + 2..].find("}}") else {
            // 没闭合：原样留下，别再找
            text.push_str(&template[open..]);
            break;
        };
        let close = open + 2 + close;
        let raw = &template[open + 2..close];
        let name = raw.trim();

        if let Some(var) = Placeholder::ALL.iter().find(|var| var.name() == name) {
            if let Some(value) = values.get(var.name()) {
                text.push_str(value);
            }
            // 已知但没给值 ⇒ 留空（见函数注释）
        } else {
            if !unknown.iter().any(|seen| seen == name) {
                unknown.push(name.to_owned());
            }
            text.push_str(&template[open..close + 2]); // 原样
        }
        cursor = close + 2;
        let _ = bytes;
    }

    Rendered { text, unknown }
}

/// 现在的时间，形如 `2026-09-27 20:04 UTC`。
///
/// 不引 `chrono` / `time`：项目里只用 `std::time`。这里是标准的"epoch 天数 → 民用日期"
/// 算法（Howard Hinnant 那套），UTC、无闰秒，够 RPG 用。**将来要本地时区再单独加** ——
/// 值里带 `UTC` 字样就是不想让人误以为它是本地时间。
pub fn system_time_utc() -> String {
    let secs = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map(|d| d.as_secs() as i64)
        .unwrap_or(0);
    let days = secs.div_euclid(86_400);
    let seconds_of_day = secs.rem_euclid(86_400);
    let (hour, minute) = (seconds_of_day / 3600, (seconds_of_day % 3600) / 60);

    // civil_from_days：把 1970-01-01 起的天数换成 (年, 月, 日)
    let z = days + 719_468;
    let era = if z >= 0 { z } else { z - 146_096 } / 146_097;
    let doe = z - era * 146_097;
    let yoe = (doe - doe / 1460 + doe / 36_524 - doe / 146_096) / 365;
    let year = yoe + era * 400;
    let doy = doe - (365 * yoe + yoe / 4 - yoe / 100);
    let mp = (5 * doy + 2) / 153;
    let day = doy - (153 * mp + 2) / 5 + 1;
    let month = if mp < 10 { mp + 3 } else { mp - 9 };
    let year = if month <= 2 { year + 1 } else { year };

    format!("{year:04}-{month:02}-{day:02} {hour:02}:{minute:02} UTC")
}

/// 把一张变量表渲染成 `键 = 值，键 = 值`（空表 ⇒ `（空）`）——
/// 摘要模板里的 `{{state_before}} / {{state_after}}` 用这个填。
pub fn render_state(state: &BTreeMap<String, String>) -> String {
    if state.is_empty() {
        return "（空）".to_owned();
    }
    state
        .iter()
        .map(|(key, value)| format!("{key} = {value}"))
        .collect::<Vec<_>>()
        .join("，")
}

#[cfg(test)]
mod tests {
    use super::*;

    fn values() -> BTreeMap<&'static str, String> {
        let mut values = BTreeMap::new();
        values.insert("range", "第 1–8 条".to_owned());
        values.insert("blocks", "3".to_owned());
        values.insert("state_before", "HP = 12".to_owned());
        values
    }

    #[test]
    fn known_variables_are_replaced() {
        let out = render(
            "区间 {{range}}，共 {{blocks}} 块；之前 {{state_before}}；现在 {{system_time}}。",
            &values(),
        );
        assert!(out.unknown.is_empty());
        assert!(out.text.contains("区间 第 1–8 条"));
        assert!(out.text.contains("共 3 块"));
        assert!(out.text.contains("之前 HP = 12"));
        assert!(!out.text.contains("{{"), "已知变量都该被换掉：{}", out.text);
    }

    /// 已知变量但没给值 ⇒ **留空**（绝不把 `{{...}}` 喂给模型）。
    #[test]
    fn known_variable_without_value_becomes_empty() {
        let out = render("之前 {{state_after}} 之后", &values());
        assert_eq!(out.text, "之前  之后");
        assert!(out.unknown.is_empty());
    }

    /// 不认识的变量：**原样留着**并回报（界面据此提示）。
    #[test]
    fn unknown_variables_are_left_alone_and_reported() {
        let out = render("{{system_time}} / {{我自己编的}} / {{我自己编的}}", &values());
        assert!(out.text.contains("{{我自己编的}}"), "原样保留");
        assert_eq!(out.unknown, vec!["我自己编的".to_owned()], "同一个名字只报一次");
        assert!(!out.text.contains("{{system_time}}"), "认识的照常换");
    }

    /// **只扫一遍**：值里写的 `{{...}}` 不再展开（否则用户文本就能玩坏注入）。
    #[test]
    fn replacement_is_not_rescanned() {
        let mut values = values();
        values.insert("range", "{{state_after}}".to_owned());
        values.insert("state_after", "不该出现".to_owned());
        let out = render("{{range}}", &values);
        assert_eq!(out.text, "{{state_after}}", "替换结果原样落地，不再当模板");
        assert!(out.unknown.is_empty(), "也不该把值里的名字记成未知");
    }

    /// 没闭合的 `{{`：原样留着（它多半只是正文）。
    #[test]
    fn unterminated_braces_are_left_alone() {
        let out = render("他用 {{ 打了个比方", &values());
        assert_eq!(out.text, "他用 {{ 打了个比方");
        assert!(out.unknown.is_empty());
    }

    /// 时间是个像样的 UTC 串（不引时间库，就自己算）。
    #[test]
    fn system_time_looks_like_a_date() {
        let now = system_time_utc();
        assert!(now.ends_with(" UTC"), "{now}");
        let (date, time) = now.split_once(' ').unwrap();
        let parts: Vec<&str> = date.split('-').collect();
        assert_eq!(parts.len(), 3);
        assert_eq!(parts[0].len(), 4, "四位年：{now}");
        let month: u32 = parts[1].parse().unwrap();
        assert!((1..=12).contains(&month), "{now}");
        assert!(time.contains(':'), "{now}");
    }

    #[test]
    fn render_state_is_readable() {
        let mut state = BTreeMap::new();
        state.insert("HP".to_owned(), "12".to_owned());
        state.insert("季节".to_owned(), "初冬".to_owned());
        let text = render_state(&state);
        assert!(text.contains("HP = 12"));
        assert!(text.contains("季节 = 初冬"));
        assert!(text.contains('，'), "多条之间用中文逗号");
        assert_eq!(render_state(&BTreeMap::new()), "（空）");
    }
}
