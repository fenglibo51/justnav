package store

import "testing"

// sanitizeURL 是挡 javascript: 这类地址的唯一一道后端防线 ——
// 前端虽然也挡，但直接打 API 就绕过了。
func TestSanitizeURL(t *testing.T) {
	cases := []struct {
		in   string
		want string
		why  string
	}{
		{"http://192.168.1.10:8096", "http://192.168.1.10:8096", "普通内网地址"},
		{"https://example.com/a?b=1", "https://example.com/a?b=1", "带查询串"},
		{"HTTP://Example.COM", "HTTP://Example.COM", "协议大小写不敏感，原样保留"},
		{"/assets/logo.png", "/assets/logo.png", "站内相对路径"},
		{"#group", "#group", "锚点"},
		{"?q=1", "?q=1", "查询串"},

		{"javascript:alert(1)", "", "经典 XSS"},
		{"JavaScript:alert(1)", "", "大小写混写"},
		{"  javascript:alert(1)", "", "前面带空格"},
		{"java\nscript:alert(1)", "", "中间插换行"},
		{"java\tscript:alert(1)", "", "中间插制表符"},
		{"data:text/html,<script>", "", "data 协议"},
		{"vbscript:msgbox(1)", "", "vbscript"},
		{"file:///etc/passwd", "", "file 协议"},
		{"", "", "空串"},
		{"   ", "", "只有空白"},
		{"ftp://x/y", "", "不支持的协议"},
		{"//evil.com/x", "", "协议相对地址（会被当成外部站）"},
	}

	for _, c := range cases {
		got := sanitizeURL(c.in)
		if got != c.want {
			t.Errorf("sanitizeURL(%q) = %q, want %q（%s）", c.in, got, c.want, c.why)
		}
	}
}

// 单条 Upsert 允许先建条目后补地址（前端就是这流程），但地址一旦给了就要合法
func TestUpsertSanitizesButAllowsEmpty(t *testing.T) {
	st := New(t.TempDir())
	_ = st.Load()

	// 没有地址也能建
	if err := st.Upsert(Item{Name: "无地址"}); err != nil {
		t.Fatalf("允许先建条目后补地址: %v", err)
	}

	// 有非法地址 → 被清成空
	if err := st.Upsert(Item{Name: "坏地址", Internal: "javascript:alert(1)"}); err != nil {
		t.Fatal(err)
	}
	for _, it := range st.Snapshot().Items {
		if it.Name == "坏地址" && it.Internal != "" {
			t.Errorf("javascript: 地址没被清掉: %q", it.Internal)
		}
	}
}

// 批量导入时分不清「没填」和「填错」，一律跳过 —— 少导入一条好过导进一条坏链接
func TestBulkUpsertSkipsBadURL(t *testing.T) {
	st := New(t.TempDir())
	_ = st.Load()

	added, _, err := st.BulkUpsert([]Item{
		{Name: "好的", Internal: "http://a:1"},
		{Name: "坏的", Internal: "javascript:alert(1)"},
		{Name: "缺地址"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if added != 1 {
		t.Errorf("应该只导入 1 条，实际 %d 条", added)
	}
	for _, it := range st.Snapshot().Items {
		if it.Name == "坏的" {
			t.Error("带 javascript: 地址的条目不该被导入")
		}
	}
}
