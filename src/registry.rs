//! 注册表合并视图：`providers.json`（用户配置，含密钥）× `models`（数据库里的发现态）。
//!
//! - 以 `providers.json` 为准绳——配置里没有的 provider，其库内模型是孤儿，不出现；
//! - `has_key` 只说"配了没有"，**绝不回显密钥内容**（`api_key` 根本不进这个视图）。

use std::collections::BTreeMap;

use serde::{Deserialize, Serialize};

use crate::config::{ProviderKind, ProvidersConfig};
use crate::model::ModelEntry;
use crate::store::{Result, Store};

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ModelView {
    pub upstream_id: String,
    /// 显示名（三级回退后的结果：用户覆盖 → 上游名 → prettify）。
    pub name: String,
    pub upstream_name: Option<String>,
    pub owned_by: Option<String>,
    pub context_length: Option<i64>,
    pub max_output: Option<i64>,
    /// 用户覆盖的"模型上下文"（`None` = 没覆盖；优先于 `context_length`）。
    pub context_override: Option<i64>,
    /// 用户覆盖的显示名（未覆盖时是 `None`；`name` 已是三级回退后的结果）。
    pub display_name: Option<String>,
    /// 用户覆盖的采样参数。
    pub params: serde_json::Value,
    /// 上游声明的参数信息（`{supported, default}`，可能为空对象）。
    pub upstream_params: serde_json::Value,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ProviderView {
    pub id: String,
    /// 配置里的显示名（可选）；客户端缺省时回退到 `id`。
    pub name: Option<String>,
    pub kind: ProviderKind,
    pub base_url: String,
    pub headers: BTreeMap<String, String>,
    /// 是否配了密钥（`providers.json` 里的 `api_key` 非空；**不回显内容**）。
    pub has_key: bool,
    /// `None` = 从未刷新过。
    pub last_refresh_at: Option<i64>,
    pub models: Vec<ModelView>,
}

pub fn views(store: &Store, config: &ProvidersConfig) -> Result<Vec<ProviderView>> {
    config
        .providers
        .iter()
        .map(|provider| {
            let last_refresh_at = store.last_refresh_at(&provider.id)?;
            // dummy provider 的模型是虚拟的：没有上游，也就没有可发现的东西，不进库。
            let models = match provider.kind {
                ProviderKind::Dummy => vec![dummy_model_view()],
                ProviderKind::OpenAiCompat => store
                    .list_models(&provider.id)?
                    .into_iter()
                    .map(model_view)
                    .collect(),
            };
            Ok(ProviderView {
                id: provider.id.clone(),
                name: provider.name.clone(),
                kind: provider.kind,
                base_url: provider.base_url.clone(),
                headers: provider.headers.clone(),
                has_key: !provider.api_key.is_empty(),
                last_refresh_at,
                models,
            })
        })
        .collect()
}

/// 扁平化后的模型条目：给"只选一个模型"的下拉用（跨 provider）。
#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ModelListItem {
    /// provider 的主键 handle（配置里的 `id`）。
    pub provider: String,
    /// provider 的显示名（可选）；缺省时客户端回退到 `provider`。
    pub provider_name: Option<String>,
    /// 上游裸模型 id，原样转发给后端用。
    pub upstream_id: String,
    /// 模型显示名（用户覆盖 → 上游名 → prettify 三级回退后的结果）。
    pub name: String,
    /// 用户覆盖的显示名（未覆盖时 `None`）。
    pub display_name: Option<String>,
}

/// 把嵌套的 provider×模型 视图拍平成一维列表；顺序 = provider 顺序 × 各自模型顺序。
pub fn model_list(views: &[ProviderView]) -> Vec<ModelListItem> {
    views
        .iter()
        .flat_map(|provider| {
            provider.models.iter().map(|model| ModelListItem {
                provider: provider.id.clone(),
                provider_name: provider.name.clone(),
                upstream_id: model.upstream_id.clone(),
                name: model.name.clone(),
                display_name: model.display_name.clone(),
            })
        })
        .collect()
}

pub fn model_view(entry: ModelEntry) -> ModelView {
    ModelView {
        upstream_id: entry.upstream_id.clone(),
        name: entry.resolved_name(),
        upstream_name: entry.upstream_name.clone(),
        owned_by: entry.owned_by.clone(),
        context_length: entry.context_length,
        max_output: entry.max_output,
        context_override: entry.context_override,
        display_name: entry.display_name.clone(),
        params: entry.params_json(),
        upstream_params: entry.upstream_params_json(),
    }
}

/// dummy 模型的展示形态：不来自数据库，而是硬编码的虚拟模型。
/// 显示名直接由回复文案拼出（回复自带全角括号），保持单一来源。
fn dummy_model_view() -> ModelView {
    ModelView {
        upstream_id: crate::chat::DUMMY_MODEL_ID.to_owned(),
        name: format!("Dummy{}", crate::chat::DUMMY_MODEL_REPLY),
        upstream_name: None,
        owned_by: None,
        context_length: None,
        max_output: None,
        context_override: None,
        display_name: None,
        params: serde_json::Value::Object(Default::default()),
        upstream_params: serde_json::Value::Object(Default::default()),
    }
}

#[cfg(test)]
mod tests {
    use crate::config::{ProviderConfig, ProviderKind, ProvidersConfig};
    use crate::model::DiscoveredModel;
    use crate::store::Store;

    use super::{model_list, views};

    fn provider(id: &str) -> ProviderConfig {
        ProviderConfig {
            id: id.to_owned(),
            base_url: format!("http://{id}/v1"),
            ..Default::default()
        }
    }

    #[test]
    fn flat_model_list_carries_provider_and_both_names() {
        let mut store = Store::open_in_memory().unwrap();
        store
            .apply_discovery(
                "open",
                &[DiscoveredModel {
                    upstream_id: "gpt-5.6-sol".to_owned(),
                    upstream_name: Some("GPT 5.6 Sol".to_owned()),
                    ..Default::default()
                }],
            )
            .unwrap();
        let config = ProvidersConfig {
            providers: vec![ProviderConfig {
                id: "open".to_owned(),
                name: Some("OpenAI".to_owned()),
                base_url: "http://open/v1".to_owned(),
                ..Default::default()
            }],
            ..Default::default()
        };

        let list = model_list(&views(&store, &config).unwrap());
        assert_eq!(list.len(), 1);
        assert_eq!(list[0].provider, "open");
        assert_eq!(list[0].provider_name.as_deref(), Some("OpenAI"));
        assert_eq!(list[0].upstream_id, "gpt-5.6-sol");
        assert_eq!(list[0].name, "GPT 5.6 Sol", "没被覆盖时用上游显示名");
        assert_eq!(list[0].display_name, None, "用户覆盖单独暴露");

        store
            .set_model_display_name("open", "gpt-5.6-sol", Some("小模型"))
            .unwrap();
        let list = model_list(&views(&store, &config).unwrap());
        assert_eq!(list[0].name, "小模型", "覆盖后 name 跟着变");
        assert_eq!(list[0].display_name.as_deref(), Some("小模型"));
    }

    #[test]
    fn flat_list_includes_the_virtual_dummy_model() {
        let store = Store::open_in_memory().unwrap();
        let config = ProvidersConfig {
            providers: vec![ProviderConfig {
                id: "dummy".to_owned(),
                kind: ProviderKind::Dummy,
                ..Default::default()
            }],
            ..Default::default()
        };
        let list = model_list(&views(&store, &config).unwrap());
        assert_eq!(list.len(), 1);
        assert_eq!(list[0].provider, "dummy");
        assert_eq!(list[0].upstream_id, crate::chat::DUMMY_MODEL_ID);
        assert_eq!(list[0].provider_name, None, "没配名字时回退由客户端做");
    }

    #[test]
    fn orphan_models_are_hidden_and_names_fall_back() {
        let mut store = Store::open_in_memory().unwrap();
        store
            .apply_discovery(
                "ghost",
                &[DiscoveredModel {
                    upstream_id: "ghost-model".to_owned(),
                    ..Default::default()
                }],
            )
            .unwrap();
        store
            .apply_discovery(
                "open",
                &[DiscoveredModel {
                    upstream_id: "deepseek/deepseek-v4-flash".to_owned(),
                    owned_by: Some("deepseek".to_owned()),
                    context_length: Some(131072),
                    supported_parameters: vec!["temperature".to_owned()],
                    ..Default::default()
                }],
            )
            .unwrap();

        let config = ProvidersConfig {
            providers: vec![provider("open")],
            ..Default::default()
        };
        let mut config = config;
        config.providers[0].api_key = "sk-x".to_owned();

        let views = views(&store, &config).unwrap();
        assert_eq!(views.len(), 1, "配置里没有的 provider 不该出现");

        let view = &views[0];
        assert_eq!(view.kind, ProviderKind::OpenAiCompat);
        assert!(view.has_key);
        assert!(view.last_refresh_at.is_some());
        assert_eq!(view.models.len(), 1);
        assert_eq!(view.models[0].name, "Deepseek V4 Flash", "无上游名时回退 prettify");
        assert_eq!(view.models[0].context_length, Some(131072));
        assert_eq!(view.models[0].upstream_params["supported"][0], "temperature");
    }

    #[test]
    fn dummy_provider_exposes_the_virtual_model() {
        let store = Store::open_in_memory().unwrap();
        let config = ProvidersConfig {
            providers: vec![ProviderConfig {
                id: "dummy".to_owned(),
                kind: ProviderKind::Dummy,
                base_url: String::new(),
                ..Default::default()
            }],
            ..Default::default()
        };

        let views = views(&store, &config).unwrap();
        assert_eq!(views[0].models.len(), 1, "dummy provider 恰好提供一个虚拟模型");
        assert_eq!(views[0].models[0].upstream_id, "dummy");
        assert_eq!(views[0].models[0].name, "Dummy（测试用空模型）");
    }

    #[test]
    fn view_reflects_the_latest_upstream_list() {
        let mut store = Store::open_in_memory().unwrap();
        store
            .apply_discovery(
                "open",
                &[DiscoveredModel {
                    upstream_id: "gone".to_owned(),
                    ..Default::default()
                }],
            )
            .unwrap();
        store
            .apply_discovery(
                "open",
                &[DiscoveredModel {
                    upstream_id: "here".to_owned(),
                    ..Default::default()
                }],
            )
            .unwrap();

        let config = ProvidersConfig {
            providers: vec![provider("open")],
            ..Default::default()
        };
        let views = views(&store, &config).unwrap();

        let ids: Vec<&str> = views[0]
            .models
            .iter()
            .map(|model| model.upstream_id.as_str())
            .collect();
        assert_eq!(ids, ["here"], "视图里应该只剩上游最新给的那份");
    }
}
