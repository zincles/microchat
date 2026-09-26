//! 注册表合并视图：`providers.jsonc`（用户配置） × `models`（数据库里的发现态） × `secrets.json`。
//!
//! - 以 `providers.jsonc` 为准绳——配置里没有的 provider，其库内模型是孤儿，不出现；
//! - `has_key` 只说"配了没有"，**绝不回显密钥内容**。

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
    /// 用户覆盖的采样参数。
    pub params: serde_json::Value,
    /// 上游声明的参数信息（`{supported, default}`，可能为空对象）。
    pub upstream_params: serde_json::Value,
}

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct ProviderView {
    pub id: String,
    pub kind: ProviderKind,
    pub base_url: String,
    pub headers: BTreeMap<String, String>,
    /// 是否已在 `secrets.json` 里配了密钥（不回显内容）。
    pub has_key: bool,
    /// `None` = 从未刷新过。
    pub last_refresh_at: Option<i64>,
    pub models: Vec<ModelView>,
}

pub fn views(
    store: &Store,
    config: &ProvidersConfig,
    secrets: &BTreeMap<String, String>,
) -> Result<Vec<ProviderView>> {
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
                kind: provider.kind,
                base_url: provider.base_url.clone(),
                headers: provider.headers.clone(),
                has_key: secrets.get(&provider.id).is_some_and(|key| !key.is_empty()),
                last_refresh_at,
                models,
            })
        })
        .collect()
}

fn model_view(entry: ModelEntry) -> ModelView {
    ModelView {
        upstream_id: entry.upstream_id.clone(),
        name: entry.resolved_name(),
        upstream_name: entry.upstream_name.clone(),
        owned_by: entry.owned_by.clone(),
        context_length: entry.context_length,
        max_output: entry.max_output,
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
        params: serde_json::Value::Object(Default::default()),
        upstream_params: serde_json::Value::Object(Default::default()),
    }
}

#[cfg(test)]
mod tests {
    use std::collections::BTreeMap;

    use crate::config::{ProviderConfig, ProviderKind, ProvidersConfig};
    use crate::model::DiscoveredModel;
    use crate::store::Store;

    use super::views;

    fn provider(id: &str) -> ProviderConfig {
        ProviderConfig {
            id: id.to_owned(),
            base_url: format!("http://{id}/v1"),
            ..Default::default()
        }
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
        let mut secrets = BTreeMap::new();
        secrets.insert("open".to_owned(), "sk-x".to_owned());

        let views = views(&store, &config, &secrets).unwrap();
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

        let views = views(&store, &config, &BTreeMap::new()).unwrap();
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
        let views = views(&store, &config, &BTreeMap::new()).unwrap();

        let ids: Vec<&str> = views[0]
            .models
            .iter()
            .map(|model| model.upstream_id.as_str())
            .collect();
        assert_eq!(ids, ["here"], "视图里应该只剩上游最新给的那份");
    }
}
