package server

import (
	"net/http"
	"strconv"
	"strings"
	"testing"

	"microchat/internal/auth"
	"microchat/internal/task"
)

// usersSandbox：root 已初始化 + 一个非 admin 的 bob（直接落库）—— 两条令牌都备好。
type usersSandbox struct {
	*dummySandbox
	rootTok string
	bobID   int64
	bobTok  string
}

func newUsersSandbox(t *testing.T) *usersSandbox {
	t.Helper()
	box := newDummySandboxWith(t, false) // 从未初始化起步：先 setup root
	res := authCall(box.server, "POST", "/api/v1/auth/setup", `{"password":"hunter22"}`, "")
	if res.Code != 200 {
		t.Fatalf("setup = %d（%s）", res.Code, res.Body.String())
	}
	rootTok := decodeBody[struct {
		Token string `json:"token"`
	}](t, res).Token

	hash, err := auth.HashPassword("hunter22")
	if err != nil {
		t.Fatal(err)
	}
	bob, err := box.store.CreateUser("bob", hash, false)
	if err != nil {
		t.Fatal(err)
	}
	bobTok, err := auth.Sign([]byte(testJWTKey), bob.ID, bob.IsAdmin, bob.Ver)
	if err != nil {
		t.Fatal(err)
	}
	return &usersSandbox{dummySandbox: box, rootTok: rootTok, bobID: bob.ID, bobTok: bobTok}
}

// 人事口只认 admin：bob 看不了列表、加不了人、改不了别人 —— 但**自己改自己**行。
func TestUsersCRUDAdminOnly(t *testing.T) {
	u := newUsersSandbox(t)

	if got := authCall(u.server, "GET", "/api/v1/users", "", u.bobTok); got.Code != http.StatusForbidden {
		t.Fatalf("bob 看用户列表 = %d，想要 403", got.Code)
	}
	if got := authCall(u.server, "POST", "/api/v1/users", `{"name":"evil","password":"hunter22"}`, u.bobTok); got.Code != http.StatusForbidden {
		t.Fatalf("bob 加人 = %d，想要 403", got.Code)
	}
	if got := authCall(u.server, "POST", "/api/v1/users/0/password", `{"password":"hunter22"}`, u.bobTok); got.Code != http.StatusForbidden {
		t.Fatalf("bob 改 root 口令 = %d，想要 403", got.Code)
	}

	// admin 加人：从 1000 起；重名 / 纯数字名 / 短口令各自 400
	created := authCall(u.server, "POST", "/api/v1/users", `{"name":"alice","password":"hunter22"}`, u.rootTok)
	if created.Code != 201 {
		t.Fatalf("root 加 alice = %d（%s）", created.Code, created.Body.String())
	}
	alice := decodeBody[struct {
		ID    int64  `json:"id"`
		Name  string `json:"name"`
		Admin bool   `json:"is_admin"`
	}](t, created)
	if alice.ID != 1001 || alice.Admin { // bob 已占 1000（uid 从 1000 起、顺序发）
		t.Fatalf("新用户形状不对：%+v", alice)
	}
	for _, body := range []string{`{"name":"alice","password":"hunter22"}`, `{"name":"1234","password":"hunter22"}`, `{"name":"x","password":"short"}`} {
		if got := authCall(u.server, "POST", "/api/v1/users", body, u.rootTok); got.Code != 400 {
			t.Fatalf("坏加人（%s）= %d，想要 400", body, got.Code)
		}
	}

	// 列表：admin 见全体（含 has_password，绝不含哈希）
	list := authCall(u.server, "GET", "/api/v1/users", "", u.rootTok)
	if list.Code != 200 || len(decodeBody[[]map[string]any](t, list)) < 3 {
		t.Fatalf("列表 = %d %s", list.Code, list.Body.String())
	}
	if body := list.Body.String(); strings.Contains(body, "pass_hash") || strings.Contains(body, "$2a$") {
		t.Fatalf("列表泄漏了口令哈希：%s", body)
	}

	// 自己改自己：bob 改自己的口令 ⇒ 204，旧票立刻失效，新口令能登
	if got := authCall(u.server, "POST", "/api/v1/users/"+itoa(u.bobID)+"/password", `{"password":"hunter33"}`, u.bobTok); got.Code != 204 {
		t.Fatalf("bob 改自己口令 = %d", got.Code)
	}
	if got := authCall(u.server, "GET", "/api/v1/auth/me", "", u.bobTok); got.Code != http.StatusUnauthorized {
		t.Fatalf("改口令后旧票 = %d，想要 401", got.Code)
	}
	bobLogin := authCall(u.server, "POST", "/api/v1/auth/login", `{"user":"bob","password":"hunter33"}`, "")
	if bobLogin.Code != 200 {
		t.Fatalf("新口令登录 = %d", bobLogin.Code)
	}
	// 换了口令 ⇒ 旧票全废：后面 bob 的断言要用**这枚新票**（旧票只会拿到 401，测不到 403）。
	bobTok2 := decodeBody[struct {
		Token string `json:"token"`
	}](t, bobLogin).Token

	// 改名：admin 改 bob ⇒ 200；纯数字 ⇒ 400；bob 改 root ⇒ 403
	if got := authCall(u.server, "PATCH", "/api/v1/users/"+itoa(u.bobID), `{"name":"bobby"}`, u.rootTok); got.Code != 200 {
		t.Fatalf("改名 = %d（%s）", got.Code, got.Body.String())
	}
	if got := authCall(u.server, "PATCH", "/api/v1/users/"+itoa(u.bobID), `{"name":"1234"}`, u.rootTok); got.Code != 400 {
		t.Fatalf("纯数字改名 = %d，想要 400", got.Code)
	}
	if got := authCall(u.server, "PATCH", "/api/v1/users/0", `{"name":"x"}`, bobTok2); got.Code != http.StatusForbidden {
		t.Fatalf("bob 改 root 名字 = %d，想要 403", got.Code)
	}
}

// **root 的口令 = CLI 专属**（用户定，2026-10-10）：网络口（**连 root 自己的票也不行**）一律 403，
// 拒绝理由要指路 CLI。这是刻意的能力缺口 —— 票可能泄漏，能换 root 口令 = 能把 root 抢走。
func TestRootPasswordNotResettableOverHTTP(t *testing.T) {
	u := newUsersSandbox(t)
	got := authCall(u.server, "POST", "/api/v1/users/0/password", `{"password":"hunter44"}`, u.rootTok)
	if got.Code != http.StatusForbidden {
		t.Fatalf("root 改 root 口令（HTTP）= %d，想要 403", got.Code)
	}
	if body := got.Body.String(); !strings.Contains(body, "set-root-password") {
		t.Fatalf("拒绝理由该指路 CLI：%s", body)
	}
	// 口令真的没被改：旧口令照样能登
	if login := authCall(u.server, "POST", "/api/v1/auth/login", `{"user":"0","password":"hunter22"}`, ""); login.Code != 200 {
		t.Fatalf("root 旧口令登录 = %d（口令不该被网络口改掉）", login.Code)
	}
	// 但**系统身份**（进程内环回令牌 = 本机 CLI，也就是 TUI 的 `/passwd`）可以
	if got := authCall(u.server, "POST", "/api/v1/users/0/password", `{"password":"hunter44"}`, testSysToken); got.Code != 204 {
		t.Fatalf("系统身份改 root 口令 = %d，想要 204（%s）", got.Code, got.Body.String())
	}
	if login := authCall(u.server, "POST", "/api/v1/auth/login", `{"user":"0","password":"hunter44"}`, ""); login.Code != 200 {
		t.Fatalf("重设后的新口令登录 = %d", login.Code)
	}
	if login := authCall(u.server, "POST", "/api/v1/auth/login", `{"user":"0","password":"hunter22"}`, ""); login.Code != http.StatusUnauthorized {
		t.Fatalf("旧口令该作废（ver+1）= %d", login.Code)
	}
}

// 删人：**对会话必须显式**（transfer|purge）；root 不许删。
func TestDeleteUserRequiresExplicitSessions(t *testing.T) {
	u := newUsersSandbox(t)

	// 给 bob 造一条会话
	hash, _ := auth.HashPassword("hunter22")
	alice, err := u.store.CreateUser("alice", hash, false)
	if err != nil {
		t.Fatal(err)
	}
	aliceTok, _ := auth.Sign([]byte(testJWTKey), alice.ID, false, alice.Ver)
	if got := authCall(u.server, "POST", "/api/v1/sessions", `{}`, aliceTok); got.Code != 201 {
		t.Fatalf("alice 建会话 = %d", got.Code)
	}

	// 缺 sessions 参数 ⇒ 400；root ⇒ 400（不许删）
	if got := authCall(u.server, "DELETE", "/api/v1/users/"+itoa(alice.ID), "", u.rootTok); got.Code != 400 {
		t.Fatalf("缺 sessions = %d，想要 400", got.Code)
	}
	if got := authCall(u.server, "DELETE", "/api/v1/users/0?sessions=transfer", "", u.rootTok); got.Code != 400 {
		t.Fatalf("删 root = %d，想要 400（root 不许删）", got.Code)
	}

	// transfer ⇒ 人没了、会话还在且归 root
	got := authCall(u.server, "DELETE", "/api/v1/users/"+itoa(alice.ID)+"?sessions=transfer", "", u.rootTok)
	if got.Code != 200 {
		t.Fatalf("transfer 删人 = %d（%s）", got.Code, got.Body.String())
	}
	if user, _ := u.store.GetUserByID(alice.ID); user != nil {
		t.Fatal("人该没了")
	}
	list, _ := u.store.ListSessions(0)
	found := false
	for _, s := range list {
		if s.OwnerID == 0 {
			found = true
		}
	}
	if !found {
		t.Fatal("transfer 后会话该归 root")
	}

	// purge ⇒ 会话一起没
	carol, err := u.store.CreateUser("carol", hash, false)
	if err != nil {
		t.Fatal(err)
	}
	carolTok, _ := auth.Sign([]byte(testJWTKey), carol.ID, false, carol.Ver)
	created := decodeBody[struct {
		ID string `json:"id"`
	}](t, authCall(u.server, "POST", "/api/v1/sessions", `{}`, carolTok))
	got = authCall(u.server, "DELETE", "/api/v1/users/"+itoa(carol.ID)+"?sessions=purge", "", u.rootTok)
	if got.Code != 200 {
		t.Fatalf("purge 删人 = %d", got.Code)
	}
	if session, _ := u.store.GetSession(created.ID); session != nil {
		t.Fatal("purge 后会话该没了")
	}
}

// **设置 = root 专属**（用户定）：非 admin 碰设置专属读口一律 403；聊天要用的读口照常。
func TestSettingsEndpointsAreAdminOnly(t *testing.T) {
	u := newUsersSandbox(t)
	for _, path := range []string{"/api/v1/config/chat", "/api/v1/config/telegram", "/api/v1/providers", "/api/v1/providers/presets", "/api/v1/debug/last-payload"} {
		if got := authCall(u.server, "GET", path, "", u.bobTok); got.Code != http.StatusForbidden {
			t.Fatalf("bob GET %s = %d，想要 403", path, got.Code)
		}
	}
	if got := authCall(u.server, "POST", "/api/v1/sessions/import-st", `{"mes":"x"}`, u.bobTok); got.Code != http.StatusForbidden {
		t.Fatalf("bob 导入 = %d，想要 403", got.Code)
	}
	// 聊天界面要用的：照常
	for _, path := range []string{"/api/v1/agents", "/api/v1/models", "/api/v1/config/defaults", "/api/v1/tasks", "/api/v1/sessions"} {
		if got := authCall(u.server, "GET", path, "", u.bobTok); got.Code != 200 {
			t.Fatalf("bob GET %s = %d，想要 200", path, got.Code)
		}
	}
	// admin 照常进设置
	if got := authCall(u.server, "GET", "/api/v1/config/chat", "", u.rootTok); got.Code != 200 {
		t.Fatalf("root GET /config/chat = %d", got.Code)
	}
	// /tasks：非 admin 看不见别人会话的任务（此刻只有 root 的会话 + 尚无任务 ⇒ 空板也对）
	if got := authCall(u.server, "GET", "/api/v1/tasks", "", u.bobTok); got.Code != 200 {
		t.Fatalf("bob /tasks = %d", got.Code)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// /tasks：非 admin 只看见**自己会话**的任务（面板跨用户，别拿别人的会话打招呼）。
func TestTaskBoardFiltersByOwner(t *testing.T) {
	u := newUsersSandbox(t)
	// sandbox 的默认会话归 root（owner 0）：在它上面挂一个任务
	g := u.server.tasks.Begin(task.KindTitle, &u.sessionID, "起标题 · root 的会话")
	defer g.Interrupted() // 幂等兜底收尾（面板上不留僵尸）

	rootBoard := decodeBody[task.Board](t, authCall(u.server, "GET", "/api/v1/tasks", "", u.rootTok))
	if rootBoard.Running != 1 || len(rootBoard.Tasks) != 1 {
		t.Fatalf("root 面板 = %+v，想要 1 条在跑", rootBoard)
	}
	bobBoard := decodeBody[task.Board](t, authCall(u.server, "GET", "/api/v1/tasks", "", u.bobTok))
	if bobBoard.Running != 0 || len(bobBoard.Tasks) != 0 {
		t.Fatalf("bob 面板 = %+v，想要空的（那不是他的会话）", bobBoard)
	}
}

// **TUI（系统身份）= root 登录**（用户定，2026-10-10）：列表、归属、我是谁全按 uid 0 算 ——
// 不是"随便都能看的上帝视角"。这条钉住：系统身份看见的列表 == root 自己的 JWT 看见的那份。
func TestSystemIdentityEqualsRootLogin(t *testing.T) {
	u := newUsersSandbox(t)
	// 造一条"别人的会话"：它绝不该出现在 TUI/root 的列表里
	hash, err := auth.HashPassword("hunter22")
	if err != nil {
		t.Fatal(err)
	}
	alice, err := u.store.CreateUser("alice", hash, false)
	if err != nil {
		t.Fatal(err)
	}
	other, err := u.store.CreateSession(alice.ID, "dummy", "dummy", "", "")
	if err != nil {
		t.Fatal(err)
	}

	idsOf := func(token string) map[string]bool {
		list := authCall(u.server, "GET", "/api/v1/sessions", "", token)
		if list.Code != 200 {
			t.Fatalf("列表 = %d（%s）", list.Code, list.Body.String())
		}
		out := map[string]bool{}
		for _, s := range decodeBody[[]struct {
			ID string `json:"id"`
		}](t, list) {
			out[s.ID] = true
		}
		return out
	}
	sysIDs, rootIDs := idsOf(testSysToken), idsOf(u.rootTok)
	if sysIDs[other.ID] || rootIDs[other.ID] {
		t.Fatalf("别人的会话不该出现在 TUI/root 的列表里：%v / %v", sysIDs, rootIDs)
	}
	if !sysIDs[u.sessionID] || !rootIDs[u.sessionID] {
		t.Fatalf("root 自己的会话该都在：%v / %v", sysIDs, rootIDs)
	}
	if len(sysIDs) != len(rootIDs) {
		t.Fatalf("系统身份看见的列表该与 root 的 JWT 一模一样：%d vs %d", len(sysIDs), len(rootIDs))
	}
	// "我是谁"：回 root（带 sys 标记）
	me := authCall(u.server, "GET", "/api/v1/auth/me", "", testSysToken)
	if me.Code != 200 || !strings.Contains(me.Body.String(), `"id":0`) || !strings.Contains(me.Body.String(), `"sys":true`) {
		t.Fatalf("系统身份的 me 该是 root+sys：%s", me.Body.String())
	}
}
