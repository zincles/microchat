extends Label
## 设备探针：把 DisplayHelper.get_debug_text() 的结果实时显示在这个 Label 上，
## 并附上底部输入框的实际状态（已施加的变换、焦点、矩形）——"该抬多少"与"实际抬了多少"对照着看。
##
## 只是开发期的仪表：正式设计里不需要它时，删掉本文件 + 场景里的 AvailableRegionLabel 节点即可。
##
## 值一变就 print，所以真机上 `adb logcat -s godot` 也能读。

var _last := ""
## 底部输入框：顺便报告它的位置（也方便用 adb 精确点它、把软键盘点出来）。
var _input: Control = null


func _ready() -> void:
	_input = get_node_or_null("../VBoxContainer/TextEdit") as Control


func _process(_delta: float) -> void:
	var report := _build_report()
	if report == _last:
		return
	_last = report
	text = report
	# 值一变就打印：真机上 `adb logcat -s godot` 就能看到键盘弹出/收起前后的差异
	print(report)


func _build_report() -> String:
	var extra := PackedStringArray()
	if is_instance_valid(_input):
		extra.append("已施加变换=%s  焦点=%s" % [
			_input.offset_transform_position, _input.has_focus()
		])
		var r := _input.get_global_rect()
		extra.append("输入框 起点=(%d,%d) 尺寸=(%d,%d)" % [
			int(r.position.x), int(r.position.y), int(r.size.x), int(r.size.y)
		])
	return DisplayHelper.get_debug_text() + "\n" + "\n".join(extra)
