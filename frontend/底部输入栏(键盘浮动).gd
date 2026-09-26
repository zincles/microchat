extends PanelContainer
## 底部输入栏跟着软键盘浮起。
##
## 用 `offset_transform_position` 做一次 UI 变换：变换**不参与布局**，所以消息区高度不变，
## 只是让输入栏躲到键盘上方（键盘盖住的那一段重新露出来）。
##
## 平台差异全部在 `DisplayHelper` 里（Android 引擎给键盘高 / Web 问 visualViewport），
## 本脚本只做两件事：**像素 → ui 的换算**、**把结果施加到自身**。
##
## 桌面没有软键盘：把 `lift_override` 设成 >= 0 就能模拟抬升（在 Linux 上也能把观感调好）。

## 强制抬升量（ui 坐标）；负数 = 跟随真实软键盘。
@export var lift_override: float = -1.0

var _lift := 0.0


func _ready() -> void:
	# 这两个开关**默认都是反的**：`enabled = false`、`visual_only = true`。
	# 只把 enabled 打开而不动 visual_only，会变成"看得见、点不到"：
	# 画面上移了，触摸判定却留在原地（正好落在键盘上）。
	offset_transform_enabled = true
	offset_transform_visual_only = false


func _process(_delta: float) -> void:
	var lift := current_lift()
	if is_equal_approx(lift, _lift):
		return                     # 键盘高度会随候选栏/动画抖动，只在真正变化时写
	_lift = lift
	# 负 y = 向上移（曾在真机上实测：offset=(0,-300) → global_rect.y 1100 → 800）
	offset_transform_position = Vector2(0.0, -lift)


## 当前抬升量，单位是 **ui（canvas_items）坐标**。
##
## 换算只在这里做一次：`DisplayHelper` 给的是**像素**（与 `window_get_size()` 同一个像素空间），
## 而 `offset_transform_position` 吃的是 ui；乘 `视图高 / 窗口像素高` 才是它要的单位。
## 视图高取**当前**值 —— 本项目 `stretch/aspect = expand`，屏幕比例一变视图高就变，
## 键盘弹出时窗口尺寸也可能跟着变；读死值会算错。
func current_lift() -> float:
	if lift_override >= 0.0:
		return lift_override
	var keyboard_px := DisplayHelper.get_virtual_keyboard_size_px().y
	var window_px := float(DisplayServer.window_get_size().y)
	if keyboard_px <= 0.0 or window_px <= 0.0:
		return 0.0                 # 键盘没弹起 / 平台不支持 → 保持不动
	return keyboard_px * (get_viewport().get_visible_rect().size.y / window_px)
