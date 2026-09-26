extends Label
## 移动端「可用区域」探针：把 DisplayServer 报的原始数据实时显示出来。
##
## 用法：手机跑起来后看两遍数字——①不弹键盘 ②点输入框弹出键盘。
## 关键两行：
##   safe_area —— 官方定义是"未被遮挡、应当渲染交互控件的区域"（仅 Android/iOS 实现）
##   kb_height —— 屏上键盘高度（像素；隐藏时为 0；Android 7/8 非沉浸式首次可能报 0）
## 底部输入栏的抬升量 = max(kb_height, screen.bottom - safe_area.bottom)，
## 再乘 px_to_ui 换算到 canvas_items 坐标（本项目 stretch/mode=canvas_items，必须换算）。
##
## 故意全用 ASCII：Godot 内置字体没有中文字形，诊断信息用中文会变方块（顺带可验证这点）。

var _last := ""
## 本后端是否支持软键盘。**必须**先判断再调用 `virtual_keyboard_get_height()`：
## 不支持的平台（桌面/无头）每次调用都会打一条 WARNING，逐帧刷屏。
var _has_vk := DisplayServer.has_feature(DisplayServer.FEATURE_VIRTUAL_KEYBOARD)
## 用来报告输入框在屏幕上的位置（顺便方便用 adb 精确点它、把键盘点出来）。
var _input: Control = null
var _icon: Control = null


func _ready() -> void:
	_input = get_node_or_null("../VBoxContainer/TextEdit") as Control
	_icon = get_node_or_null("../Icon") as Control


func _process(_delta: float) -> void:
	var report := _build_report()
	if report == _last:
		return
	_last = report
	text = report
	# 值一变就打印：真机上 `adb logcat -s godot` 就能看到键盘弹出/收起前后的差异
	print(report)


func _build_report() -> String:
	var win := DisplayServer.window_get_size()            # 像素
	var view := get_viewport().get_visible_rect().size    # canvas_items 坐标
	var screen := DisplayServer.screen_get_size()         # 像素
	var safe := DisplayServer.get_display_safe_area()
	var usable := DisplayServer.screen_get_usable_rect()
	var kb := DisplayServer.virtual_keyboard_get_height() if _has_vk else 0 # 像素
	var scale := view.y / float(win.y) if win.y > 0 else 1.0

	var insets := Vector4i(
		safe.position.x,
		safe.position.y,
		screen.x - safe.end.x,
		screen.y - safe.end.y
	)
	var lift := maxi(kb, insets.w) * scale

	var lines := PackedStringArray()
	lines.append("ds=%s  os=%s" % [DisplayServer.get_name(), OS.get_name()])
	lines.append("scr_scale=%.2f  px_to_ui=%.3f" % [DisplayServer.screen_get_scale(), scale])
	lines.append("window(px)=%d x %d" % [win.x, win.y])
	lines.append("viewport(ui)=%d x %d" % [int(view.x), int(view.y)])
	lines.append("screen(px)=%d x %d" % [screen.x, screen.y])
	lines.append("safe_area  pos=(%d,%d) size=(%d,%d)" % [
		safe.position.x, safe.position.y, safe.size.x, safe.size.y
	])
	lines.append("  insets  L/T/R/B = %d / %d / %d / %d" % [
		insets.x, insets.y, insets.z, insets.w
	])
	lines.append("usable_rect pos=(%d,%d) size=(%d,%d)" % [
		usable.position.x, usable.position.y, usable.size.x, usable.size.y
	])
	lines.append("kb_height(px)=%d  ->(ui)=%.1f" % [kb, kb * scale])
	lines.append("lift_for_bottom_bar(ui)=%.1f" % lift)
	lines.append("features  vk=%s  hw_kbd=%s  touch=%s" % [
		_has_vk,
		DisplayServer.has_hardware_keyboard(),
		DisplayServer.is_touchscreen_available(),
	])
	if is_instance_valid(_input):
		lines.append("transform_offset=%s  focus=%s" % [
			_input.offset_transform_position, _input.has_focus()
		])
	if is_instance_valid(_icon):
		var ir := _icon.get_global_rect()
		lines.append("icon_rect pos=(%d,%d) size=(%d,%d)" % [
			int(ir.position.x), int(ir.position.y), int(ir.size.x), int(ir.size.y)
		])
	if is_instance_valid(_input):
		var r := _input.get_global_rect()
		lines.append("input_box pos=(%d,%d) size=(%d,%d)" % [
			int(r.position.x), int(r.position.y), int(r.size.x), int(r.size.y)
		])
	else:
		lines.append("input_box 未找到（节点路径变了？）")
	return "\n".join(lines)
