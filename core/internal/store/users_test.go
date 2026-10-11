package store

import "testing"

// 规矩（DEFINE「用户与鉴权」）：**建库即种 uid 0 = root**（空口令等 setup）、"无主"的会话
// 归属在 owner_id 默认值 0 上——两条都要在存储层钉住，别靠调用方自觉。
func TestRootSeededAndSetupRunsOnce(t *testing.T) {
	st := openTemp(t)

	root, err := st.GetUserByID(0)
	if err != nil || root == nil {
		t.Fatalf("fresh 库应当已有 root（uid 0）：%v %v", root, err)
	}
	if root.Name != "root" || !root.IsAdmin || root.PassHash != "" {
		t.Fatalf("root 的种子形状不对：%+v", root)
	}
	needs, err := st.RootNeedsSetup()
	if err != nil || !needs {
		t.Fatalf("root 应待初始化：%v %v", needs, err)
	}

	if err := st.SetRootPassword("hash-1"); err != nil {
		t.Fatal(err)
	}
	after, _ := st.GetUserByID(0)
	if after.PassHash != "hash-1" || after.Ver != root.Ver+1 {
		t.Fatalf("设完口令应换哈希并 ver+1：%+v", after)
	}
	if needs, _ := st.RootNeedsSetup(); needs {
		t.Fatal("设完口令後不该还要 setup")
	}
	if err := st.SetRootPassword("hash-2"); err == nil {
		t.Fatal("第二次 setup 该报错（只许一次）")
	}
	if err := st.SetRootPassword(""); err == nil {
		t.Fatal("空口令该被挡")
	}

	// 新建会话不标 owner ⇒ 就是 root 的（owner_id 默认 0）
	session, err := st.CreateSession(0, "dummy", "dummy", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if session.OwnerID != 0 {
		t.Fatalf("无主会话应归 root（0），得到 %d", session.OwnerID)
	}
}

// CLI 专属的"重设 root 口令"：**无视现状**（已有口令也能再换）、每次 `ver+1`；空口令挡掉。
// 与 SetRootPassword（setup 的"只开一次"）是两条语义不同的路。
func TestResetRootPasswordUnconditional(t *testing.T) {
	st := openTemp(t)
	if err := st.SetRootPassword("hash-1"); err != nil {
		t.Fatal(err)
	}
	before, _ := st.GetUserByID(0)
	if err := st.ResetRootPassword("hash-2"); err != nil {
		t.Fatal(err)
	}
	after, _ := st.GetUserByID(0)
	if after.PassHash != "hash-2" || after.Ver != before.Ver+1 {
		t.Fatalf("重设应换哈希并 ver+1：%+v", after)
	}
	if needs, _ := st.RootNeedsSetup(); needs {
		t.Fatal("重设之后不该回到 setup 态")
	}
	if err := st.ResetRootPassword(""); err == nil {
		t.Fatal("空口令该被挡")
	}
}

// uid 分配：root 恒 0，其他用户**从 1000 起**（Linux 味）；ResetUsers 后仍是 1000。
// ResetUsers：非 root 用户连同其会话一锅端、root 清回无口令（服务回到"未启用"）。
func TestUserIDsFrom1000AndResetUsers(t *testing.T) {
	st := openTemp(t)
	if err := st.SetRootPassword("h"); err != nil {
		t.Fatal(err)
	}
	alice, err := st.CreateUser("alice", "h1", false)
	if err != nil {
		t.Fatal(err)
	}
	if alice.ID != 1000 {
		t.Fatalf("第一个普通用户该是 1000，得到 %d", alice.ID)
	}
	bob, err := st.CreateUser("bob", "h2", false)
	if err != nil {
		t.Fatal(err)
	}
	if bob.ID != 1001 {
		t.Fatalf("第二个该是 1001，得到 %d", bob.ID)
	}
	// 各造一条会话（root 的一条也不该被重置带走）
	if _, err := st.CreateSession(0, "dummy", "dummy", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateSession(alice.ID, "dummy", "dummy", "", ""); err != nil {
		t.Fatal(err)
	}

	users, sessions, err := st.ResetUsers()
	if err != nil {
		t.Fatal(err)
	}
	if users != 2 || sessions != 1 {
		t.Fatalf("该删 2 人 / 1 会话，得到 %d / %d", users, sessions)
	}
	if root, _ := st.GetUserByID(0); root == nil || root.PassHash != "" {
		t.Fatalf("root 该恒在且清回无口令：%+v", root)
	}
	if needs, _ := st.RootNeedsSetup(); !needs {
		t.Fatal("重置后应回到 setup 态")
	}
	if got, _ := st.ListSessions(0); len(got) != 1 {
		t.Fatalf("root 自己的会话不该动，得到 %d 条", len(got))
	}
	if again, err := st.CreateUser("carol", "h3", false); err != nil || again.ID != 1000 {
		t.Fatalf("重置后再发人仍从 1000 起：%+v %v", again, err)
	}
	// 用户名不许纯数字（数字是 uid 的地盘：登录时全数字按 uid 查）
	if _, err := st.CreateUser("1001", "h4", false); err == nil {
		t.Fatal("纯数字名字该被拒")
	}
}
