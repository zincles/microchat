extends Control
## 底部输入栏跟着软键盘浮起：用 Godot 4.8 的 `offset_transform_position` 做一次 UI 变换。
## 变换**不参与布局**，所以不会挤动 VBox 里的其它节点。
##
## 键盘尺寸与平台差异都在 `DisplayHelper` 里；本脚本只负责"换算 + 施加变换"。
## 桌面没有软键盘：把 `lift_override` 设成 >= 0 即可模拟，Linux 上也能把布局调好。

## 强制抬升量（ui 坐标）；负数 = 跟随真实键盘。
@export var lift_override: float = -1.0
## 要浮起的控件（键盘弹起时整体上移）。
@export var follow_paths: Array[NodePath] = [
	^"VBoxContainer/TextEdit",
]

var _targets: Array[Control] = []
var _lift := 0.0


func _ready() -> void:
	for path in follow_paths:
		var node := get_node_or_null(path) as Control
		if node == null:
			push_warning("找不到要浮起的节点：%s" % path)
			continue
		# Control 的这两个开关**默认都是反的**：`enabled=false`、`visual_only=true`。
		# 只打开 enabled 而不动 visual_only，会变成"看得见、点不到"。
		node.offset_transform_enabled = true
		node.offset_transform_visual_only = false
		_targets.append(node)


func _process(_delta: float) -> void:
	var lift := current_lift()
	if is_equal_approx(lift, _lift):
		return                      # 只在变化时写变换（键盘尺寸会随候选栏抖动）
	_lift = lift
	for target in _targets:
		# 负 y = 向上移（实测：offset=(0,-300) → global_rect.y 1100→800）
		target.offset_transform_position = Vector2(0.0, -lift)


## 当前抬升量（ui 坐标）。像素 → ui 的换算**只有这一处**：
## `get_virtual_keyboard_size_px()` 与 `window_get_size()` 是同一个像素空间，
## 乘 `视图高 / 窗口像素高` 即得 ui。
## 视图高要用**当前**值——本项目 aspect=expand，实际视图高会随屏幕比例变。
func current_lift() -> float:
	if lift_override >= 0.0:
		return lift_override
	var kb := DisplayHelper.get_virtual_keyboard_size_px()
	var window_px_h := float(DisplayServer.window_get_size().y)
	if kb.y <= 0.0 or window_px_h <= 0.0:
		return 0.0
	return kb.y * (get_viewport().get_visible_rect().size.y / window_px_h)
