package weather

import (
	"context"
	"os"
	"testing"
)

// TestFetchCNLive 真实网络测试：验证中国天气网源能拿到数据。
// 默认跳过（避免离线/断网时污染 go test 和 selfcheck.sh），需要时手动跑：
//   JUSTNAV_TEST_NET=1 go test ./internal/weather -run TestFetchCNLive -v
func TestFetchCNLive(t *testing.T) {
	if os.Getenv("JUSTNAV_TEST_NET") == "" {
		t.Skip("跳过网络测试；需要时设 JUSTNAV_TEST_NET=1")
	}
	c := New()
	now, err := c.Current(context.Background(), "宁波", 29.8683, 121.5440)
	if err != nil {
		t.Fatalf("取天气失败: %v", err)
	}
	t.Logf("城市=%s 温度=%.1f 天气=%s 码=%d 湿度=%d 最高=%.0f 最低=%.0f",
		now.City, now.Temp, now.Text, now.Code, now.Humidity, now.Max, now.Min)
	if now.City == "" || now.Temp == 0 {
		t.Errorf("数据不完整")
	}
}

// TestCNWMOMapping 天气描述 → 标准天气码（前端 wxIcon 依赖这套码）
func TestCNWMOMapping(t *testing.T) {
	cases := []struct {
		text string
		want int
	}{
		{"晴", 0}, {"多云", 2}, {"阴", 3},
		{"小雨", 61}, {"中雨", 63}, {"大雨", 65}, {"暴雨", 65},
		{"阵雨", 80}, {"雷阵雨", 95},
		{"小雪", 71}, {"中雪", 73}, {"大雪", 75}, {"阵雪", 85},
		{"雨夹雪", 68}, {"雾", 45}, {"霾", 45}, {"浮尘", 45},
	}
	for _, c := range cases {
		if got := cnWMO(c.text, ""); got != c.want {
			t.Errorf("cnWMO(%q) = %d, 期望 %d", c.text, got, c.want)
		}
	}
	// 描述缺失时退回编码
	if got := cnWMO("", "d00"); got != 0 {
		t.Errorf("编码兜底 d00 应为晴(0), got %d", got)
	}
	if got := cnWMO("", "d01"); got != 2 {
		t.Errorf("编码兜底 d01 应为多云(2), got %d", got)
	}
}

// TestCNExtractVar 从 JS 文本里抠出 JSON 对象（含多嵌套）
func TestCNExtractVar(t *testing.T) {
	s := `var cityDZ ={"weatherinfo":{"temp":"9","tempn":"6"}};var alarmDZ ={"w":[]};var dataSK ={"temp":"13.4"};`
	if got := cnExtractVar(s, "dataSK"); got != `{"temp":"13.4"}` {
		t.Errorf("dataSK 抠错: %q", got)
	}
	if got := cnExtractVar(s, "cityDZ"); got != `{"weatherinfo":{"temp":"9","tempn":"6"}}` {
		t.Errorf("cityDZ 多嵌套抠错: %q", got)
	}
	if got := cnExtractVar(s, "nothere"); got != "" {
		t.Errorf("不存在的变量应返回空, got %q", got)
	}
}

// TestCNParseCode 城市搜索响应 → 城市代码
func TestCNParseCode(t *testing.T) {
	body := []byte(`([{"ref":"101210401~zhejiang~宁波~Ningbo~..."},...])`)
	if got := cnParseCode(body); got != "101210401" {
		t.Errorf("cnParseCode = %q", got)
	}
	if got := cnParseCode([]byte("无结果")); got != "" {
		t.Errorf("空响应应返回空, got %q", got)
	}
	// 防止把非数字塞进来
	if got := cnParseCode([]byte(`[{"ref":"abc~x~y"}]`)); got != "" {
		t.Errorf("非数字代码应被拒, got %q", got)
	}
}
