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
