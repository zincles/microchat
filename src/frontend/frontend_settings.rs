//! 前端设置：只存在本机（`$XDG_CONFIG_HOME/microchat/frontend.json`），不上传后端。
//!
//! 与服务器设置的分工：凡是"后端持有的状态"（providers、模型、agents、生成参数）都归
//! 服务器设置，经 API 读写；这里只放界面偏好与连接信息。
//! **密码不落盘**，只在内存里活到本次运行结束。

use std::path::PathBuf;

use serde::{Deserialize, Serialize};

use microchat::config;

#[derive(Debug, Clone, Copy, PartialEq, Eq, Default, Serialize, Deserialize)]
#[serde(rename_all = "kebab-case")]
pub enum Theme {
    #[default]
    System,
    Light,
    Dark,
}

impl Theme {
    pub fn label(self) -> &'static str {
        match self {
            Self::System => "跟随系统",
            Self::Light => "浅色",
            Self::Dark => "深色",
        }
    }

    pub fn to_egui(self) -> eframe::egui::ThemePreference {
        match self {
            Self::System => eframe::egui::ThemePreference::System,
            Self::Light => eframe::egui::ThemePreference::Light,
            Self::Dark => eframe::egui::ThemePreference::Dark,
        }
    }
}

#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct FrontendSettings {
    /// 上次连接的后端地址（连接页也用它预填）。
    pub server_address: String,
    pub username: String,
    pub theme: Theme,
    pub zoom: f32,
    pub enter_sends: bool,
    pub stick_to_bottom: bool,
}

impl Default for FrontendSettings {
    fn default() -> Self {
        Self {
            server_address: super::DEFAULT_SERVER.to_owned(),
            username: String::new(),
            theme: Theme::default(),
            zoom: 1.0,
            enter_sends: true,
            stick_to_bottom: true,
        }
    }
}

impl FrontendSettings {
    pub fn path() -> PathBuf {
        let base = std::env::var_os("XDG_CONFIG_HOME")
            .map(PathBuf::from)
            .or_else(|| std::env::var_os("HOME").map(|home| PathBuf::from(home).join(".config")))
            .unwrap_or_else(|| PathBuf::from("."));
        base.join("microchat").join("frontend.json")
    }

    pub fn load() -> Self {
        config::load_json(&Self::path()).unwrap_or_default()
    }

    pub fn save(&self) -> Result<(), String> {
        config::write_json_pretty(&Self::path(), self).map_err(|e| e.to_string())
    }
}
