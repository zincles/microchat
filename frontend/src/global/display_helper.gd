extends Node
class_name DisplayHelper
## 显示 / 输入法相关的静态工具。只暴露两个函数：
##
##   var kb_px := DisplayHelper.get_virtual_keyboard_size_px()
##   var offset_ui := kb_px.y * (视图高 / 窗口像素高)     # 求偏移的固定写法，别省换算
##
##   DisplayHelper.get_debug_text()                      # 调试信息（多行字符串）


## 软键盘的尺寸，单位是**像素**，且与 `DisplayServer.window_get_size()` 同一个像素空间。
## 键盘未弹出、平台不支持（桌面）、或读不到时返回 `Vector2.ZERO`。
##
## 两个平台的来源不同，这里已经把单位统一掉了，调用方不必再分辨：
## - Android：引擎 `virtual_keyboard_get_height()` 直接给这个像素空间的键盘高（宽度为 0）；
## - Web：引擎侧是**桩**（返回 0 且每次调用打 WARNING），只能问浏览器 `visualViewport`，
##   拿到的是 CSS 像素，再按 `窗口像素高 / window.innerHeight` 折算到同一个空间。
##
## 注意 `offset_transform_position` 吃的是 **ui（canvas_items）坐标**，不是像素；
## 所以调用方必须再乘 `视图高 / 窗口像素高`（视图高要用**当前**值：aspect=expand 下它会变）。
static func get_virtual_keyboard_size_px() -> Vector2:
	if OS.has_feature("web"):
		var web := _web_keyboard_snapshot()
		if not web.get("ok", false):
			return Vector2.ZERO
		var css_kb := float(web.get("kb", 0.0))
		var inner := float(web.get("inner", 0.0))
		var window_px_h := float(DisplayServer.window_get_size().y)
		if css_kb <= 0.0 or inner <= 0.0 or window_px_h <= 0.0:
			return Vector2.ZERO
		return Vector2(0.0, css_kb * (window_px_h / inner))
	if not DisplayServer.has_feature(DisplayServer.FEATURE_VIRTUAL_KEYBOARD):
		return Vector2.ZERO
	return Vector2(0.0, float(DisplayServer.virtual_keyboard_get_height()))


## 调试信息：平台、尺寸、换算比、安全区、软键盘，以及 Web 上浏览器的原始数值。
static func get_debug_text() -> String:
	var win := DisplayServer.window_get_size()
	var view := _viewport_size()
	var screen := DisplayServer.screen_get_size()
	var safe := DisplayServer.get_display_safe_area()
	var usable := DisplayServer.screen_get_usable_rect()
	var ratio := view.y / float(win.y) if win.y > 0 else 1.0
	var kb := get_virtual_keyboard_size_px()
	var insets := Vector4i(
		safe.position.x,
		safe.position.y,
		screen.x - safe.end.x,
		screen.y - safe.end.y
	)

	var lines := PackedStringArray()
	lines.append("显示后端=%s  系统=%s" % [DisplayServer.get_name(), OS.get_name()])
	lines.append("屏幕缩放=%.2f  像素转ui比例=%.3f" % [DisplayServer.screen_get_scale(), ratio])
	lines.append("引擎窗口(px)=%d x %d" % [win.x, win.y])
	lines.append("视图(ui)=%d x %d" % [int(view.x), int(view.y)])
	lines.append("屏幕(px)=%d x %d" % [screen.x, screen.y])
	lines.append("安全区 起点=(%d,%d) 尺寸=(%d,%d)" % [
		safe.position.x, safe.position.y, safe.size.x, safe.size.y
	])
	lines.append("  边距 左/上/右/下 = %d / %d / %d / %d" % [
		insets.x, insets.y, insets.z, insets.w
	])
	lines.append("可用区 起点=(%d,%d) 尺寸=(%d,%d)" % [
		usable.position.x, usable.position.y, usable.size.x, usable.size.y
	])
	lines.append("软键盘(px)=%d  换算到ui=%.1f" % [int(kb.y), kb.y * ratio])
	lines.append("特性 软键盘=%s  物理键盘=%s  触摸=%s" % [
		DisplayServer.has_feature(DisplayServer.FEATURE_VIRTUAL_KEYBOARD),
		DisplayServer.has_hardware_keyboard(),
		DisplayServer.is_touchscreen_available(),
	])
	if OS.has_feature("web"):
		lines.append("浏览器 " + _web_snapshot_text())
	return "\n".join(lines)


## Web 快照 → 一行中文；没刷新到就说明原因。
static func _web_snapshot_text() -> String:
	var web := _web_keyboard_snapshot()
	if not web.get("ok", false):
		return "取不到 visualViewport（未刷新或浏览器不支持）"
	return "内高=%d 可视高=%d 顶部偏移=%d 键盘=%d 画布高=%d" % [
		int(web.get("inner", 0)),
		int(web.get("vvh", 0)),
		int(web.get("vvtop", 0)),
		int(web.get("kb", 0)),
		int(web.get("canvas", -1)),
	]


## 视图尺寸（ui 坐标）。`static` 里没有 `get_viewport()`，从主循环拿。
## 下划线开头 = 内部用（GDScript 没有真私有，靠约定）。
static func _viewport_size() -> Vector2:
	var loop := Engine.get_main_loop() as SceneTree
	if loop == null or loop.root == null:
		return Vector2.ZERO
	return loop.root.get_visible_rect().size


## Web 侧的键盘快照（节流缓存）。
##
## 浏览器从不暴露"键盘有多高"，只暴露后果：布局视口 `innerHeight` 不变，
## 视觉视口 `visualViewport.height` 被压缩 —— 差值就是键盘占的 CSS 像素。
##
## 跨 JS 边界有成本，所以按 `WEB_REFRESH_MS` 节流；键盘弹出动画约 200ms，
## 15Hz 足够跟上。返回值形如 {ok, kb, inner, vvh, vvtop, canvas}。
const WEB_REFRESH_MS := 66
const WEB_SNAPSHOT_JS := """(function(){
	const vv = window.visualViewport;
	if (!vv || !window.innerHeight) return JSON.stringify({ok:false});
	const canvas = document.querySelector('canvas');
	return JSON.stringify({
		ok: true,
		kb: Math.max(0, window.innerHeight - vv.height - vv.offsetTop),
		inner: window.innerHeight,
		vvh: Math.round(vv.height),
		vvtop: Math.round(vv.offsetTop),
		canvas: canvas ? canvas.clientHeight : -1
	});
})()"""

static var _last_refresh_ms := 0
static var _snapshot := {}


static func _web_keyboard_snapshot() -> Dictionary:
	var now := Time.get_ticks_msec()
	if now - _last_refresh_ms < WEB_REFRESH_MS and not _snapshot.is_empty():
		return _snapshot
	_last_refresh_ms = now
	var bridge := Engine.get_singleton("JavaScriptBridge")
	if bridge == null:
		_snapshot = {"ok": false}
		return _snapshot
	# 用单例查找而不是写 `JavaScriptBridge.eval`：后者在非 Web 平台是未声明标识符，
	# 会让整个脚本在桌面构建里解析失败。
	var raw: Variant = bridge.call("eval", WEB_SNAPSHOT_JS, true)
	var parsed: Variant = JSON.parse_string(str(raw)) if raw != null else null
	_snapshot = parsed if typeof(parsed) == TYPE_DICTIONARY else {"ok": false}
	return _snapshot
