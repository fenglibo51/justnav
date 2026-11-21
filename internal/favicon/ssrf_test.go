package favicon

import (
	"net"
	"net/url"
	"testing"
)

func mustURL(t *testing.T, s string) *url.URL {
	t.Helper()
	u, err := url.Parse(s)
	if err != nil {
		t.Fatalf("bad url %q: %v", s, err)
	}
	return u
}

// A 档策略：只挡云元数据和链路本地，内网全部放行。
//
// 之所以不封 192.168.*：JustNav 本来就是 NAS 导航页，那些正是它要访问的服务。
// 封了等于所有内网服务图标都抓不到，功能直接废掉。
func TestBlockedTarget(t *testing.T) {
	blocked := []string{
		"http://169.254.169.254/latest/meta-data/",       // AWS 元数据
		"http://169.254.169.254/",                        // 裸地址
		"http://[fd00:ec2::254]/",                        // IPv6 元数据
		"http://metadata.google.internal/computeMetadata/v1/", // GCP
		"http://169.254.1.1/",                            // 链路本地段内其他地址
	}
	allowed := []string{
		"http://192.168.6.40:32400/",   // 内网 NAS 服务 —— 必须放行
		"http://192.168.1.10:8096/",    // 内网
		"http://10.0.0.5:8080/",        // 内网
		"http://172.16.3.4:9000/",      // 内网
		"https://example.com/",         // 公网
		"http://127.0.0.1:8522/",       // 本机（用户可能真这么配）
		"http://router.local/",         // 局域网域名，静态判断放行，由 DialContext 再校验
	}

	for _, s := range blocked {
		if !blockedTarget(mustURL(t, s)) {
			t.Errorf("%s 应该被拦", s)
		}
	}
	for _, s := range allowed {
		if blockedTarget(mustURL(t, s)) {
			t.Errorf("%s 不该被拦", s)
		}
	}
}

func TestBlockedIP(t *testing.T) {
	cases := []struct {
		ip   string
		want bool
	}{
		{"169.254.169.254", true},
		{"169.254.0.1", true},
		{"169.255.0.1", false}, // 出了 /16
		{"192.168.6.40", false},
		{"10.0.0.1", false},
		{"172.16.0.1", false},
		{"8.8.8.8", false},
		{"127.0.0.1", false},
		{"::1", false},
		{"fd00:ec2::254", true},
	}
	for _, c := range cases {
		ip := net.ParseIP(c.ip)
		if ip == nil {
			t.Fatalf("解析不了 %q", c.ip)
		}
		if got := blockedIP(ip); got != c.want {
			t.Errorf("blockedIP(%s) = %v, want %v", c.ip, got, c.want)
		}
	}
}

// IPv4-mapped IPv6（::ffff:169.254.169.254）不能绕过
func TestBlockedIPv4Mapped(t *testing.T) {
	ip := net.ParseIP("::ffff:169.254.169.254")
	if ip == nil {
		t.Fatal("解析失败")
	}
	if !blockedIP(ip) {
		t.Error("IPv4-mapped 形式绕过了黑名单")
	}
}

// nil URL 当危险处理，不要 panic
func TestBlockedTargetNil(t *testing.T) {
	if !blockedTarget(nil) {
		t.Error("nil URL 应该当危险处理")
	}
}
