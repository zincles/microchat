extends Control
## 底部输入栏的"浮起"：跟着软键盘高度走，用 Godot 4.8 的 `offset_transform_position`。
##
## ── 实测结论（Galaxy Tab S9+，2560×1600，stretch=canvas_items + aspect=expand）────────
## 1. `offset_transform_position` 的单位是 **ui（父容器局部坐标）**，不是像素。
##    校准实验：把 `virtual_keyboard_get_height()` 的原始像素值(775)直接填进去，
##    Icon 与输入栏都**飞出屏幕顶部**——因为 775 ui × 2.22(≈2560/1152) = 1722 px > 屏高 1600。
##    正确做法：`775 px × px_to_ui(720/1600 = 0.45) = 348.8 ui`。
## 2. `Control` 的两个开关**默认都是反的**：`offset_transform_enabled=false`、
##    `offset_transform_visual_only=true`。光改 position 什么都不会发生；只打开 enabled
##    而不管 visual_only，则"看得见、点不到"。
##    （scene 里给 TextEdit 勾了 enabled ✓，visual_only 由本脚本兜底 ✓）
## 3. 负 y = 向上移（实测：offset=(0,-300) → global_rect.y 1100→800）。
##
## 桌面没有软键盘：`lift_override` 设成 >= 0 就能模拟（编辑器检查器里可调），
## Linux 上也能把布局调好，真机只验行为差异。

## 强制抬升量（UI 坐标）；负数 = 跟随真实键盘高度。
@export var lift_override: float = -1.0
## 一起浮起的控件：底部输入框 + 屏幕正中的参照图标（对标定很有用，不需要就从数组里删掉）。
@export var follow_paths: Array[NodePath] = [
	^"VBoxContainer/TextEdit",
	^"Icon",
]

var _targets: Array[Control] = []
var _lift := 0.0
var _has_vk := DisplayServer.has_feature(DisplayServer.FEATURE_VIRTUAL_KEYBOARD)


func _ready() -> void:
	for path in follow_paths:
		var node := get_node_or_null(path) as Control
		if node == null:
			push_warning("找不到要浮起的节点：%s" % path)
			continue
		node.offset_transform_enabled = true
		node.offset_transform_visual_only = false   # 不设的话：看得见、点不到
		_targets.append(node)


func _process(_delta: float) -> void:
	var lift := current_lift()
	if is_equal_approx(lift, _lift):
		return                      # 只在变化时写变换（键盘高度会随候选栏抖动）
	_lift = lift
	for target in _targets:
		target.offset_transform_position = Vector2(0.0, -lift)


## 当前抬升量（**UI 坐标**）：键盘高度是像素，必须按 视图高/屏幕高 换算。
func current_lift() -> float:
	if lift_override >= 0.0:
		return lift_override
	if not _has_vk:
		return 0.0
	var win_h := float(DisplayServer.window_get_size().y)
	if win_h <= 0.0:
		return 0.0
	var view_h := get_viewport().get_visible_rect().size.y
	return float(DisplayServer.virtual_keyboard_get_height()) * (view_h / win_h)
