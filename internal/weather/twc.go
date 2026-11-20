package weather

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

// The Weather Channel（weather.com）—— 苹果天气用的就是它。
//
// 为什么把它放在第一位：
//   中国天气网的 cityDZ 经常把「当天最高温」给成 999（缺测，夜间尤其常见），
//   拿不到就把最高温退回成当前温度，界面上会出现「4° / 7°」这种最高比最低还低的
//   矛盾数据（2026-11-20 实测宁波）。TWC 的 current 一次请求就带全了
//   温度、体感、湿度、天气描述、图标码、以及【真实的当日最高/最低】。
//
// 密钥说明：这是 weather.com 网页端自己用的公开 key（页面 JS 里就能拿到），
//   不是私钥。它有被限流的可能，所以挂了要有兜底源，不能只靠它。
const (
	twcCurrentURL = "https://api.weather.com/v3/wx/observations/current"
	twcAPIKey     = "71f92ea9dd2f4790b92ea9dd2f779061"
)

// fetchTWC 从 The Weather Channel 取实况 + 当日高低。
// 需要经纬度；没有坐标（用户只填了城市名）就跳过，交给中国天气网。
func (c *Client) fetchTWC(ctx context.Context, city string, lat, lon float64) (Now, error) {
	if lat == 0 && lon == 0 {
		return Now{}, fmt.Errorf("没有经纬度，TWC 用不了")
	}

	q := url.Values{}
	q.Set("geocode", strconv.FormatFloat(lat, 'f', 3, 64)+","+strconv.FormatFloat(lon, 'f', 3, 64))
	q.Set("units", "m") // 公制 → 摄氏度。默认是 e（华氏），别漏
	q.Set("language", "zh-CN")
	q.Set("format", "json")
	q.Set("apiKey", twcAPIKey)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, twcCurrentURL+"?"+q.Encode(), nil)
	if err != nil {
		return Now{}, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) "+
		"AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "application/json")

	resp, err := c.twc.Do(req)
	if err != nil {
		return Now{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Now{}, fmt.Errorf("TWC 返回 %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 128<<10))
	if err != nil {
		return Now{}, err
	}

	var d struct {
		Temp     *float64 `json:"temperature"`
		Feels    *float64 `json:"temperatureFeelsLike"`
		Humidity *int     `json:"relativeHumidity"`
		Phrase   string   `json:"wxPhraseLong"`
		Cloud    string   `json:"cloudCoverPhrase"`
		Icon     int      `json:"iconCode"`
		Max      *float64 `json:"temperatureMax24Hour"`
		Min      *float64 `json:"temperatureMin24Hour"`
	}
	if err := json.Unmarshal(body, &d); err != nil {
		return Now{}, fmt.Errorf("TWC 解析失败: %w", err)
	}
	if d.Temp == nil {
		return Now{}, fmt.Errorf("TWC 没有温度字段")
	}

	now := Now{City: city, Temp: *d.Temp, At: time.Now(), Code: twcWMO(d.Icon)}
	if d.Feels != nil {
		now.Feels = *d.Feels
	}
	if d.Humidity != nil {
		now.Humidity = *d.Humidity
	}
	// 描述优先用 wxPhraseLong（"晴朗"），空的话退到 cloudCoverPhrase（"局部多云"）
	now.Text = d.Phrase
	if now.Text == "" {
		now.Text = d.Cloud
	}
	if d.Max != nil {
		now.Max = *d.Max
	}
	if d.Min != nil {
		now.Min = *d.Min
	}
	// 高低都没给（或给反了）就把两个都置成同一个值，让前端识别成「没有高低温」而不显示
	if d.Max == nil || d.Min == nil || now.Max <= now.Min {
		now.Max, now.Min = 0, 0
	}
	return now, nil
}

// twcWMO 把 TWC 的 iconCode（1~47）折算成 WMO 天气码。
// 前端 wxIcon() 认的是 WMO 那一套（0 晴 / 2 局部多云 / 3 阴 / 45 雾 /
// 51 毛毛雨 / 61 雨 / 71 雪 / 95 雷雨），所以这里只需要归类到这几档，
// 不用逐个 iconCode 精细还原。
func twcWMO(ic int) int {
	switch {
	// 晴（含夜间 clear / mostly clear / partly cloudy-night）
	case ic == 1, ic == 2, ic == 30, ic == 31, ic == 32, ic == 33:
		return 0
	// 局部多云
	case ic == 3, ic == 4, ic == 6, ic == 34:
		return 2
	// 阴 / 多云
	case ic == 7, ic == 8, ic == 35:
		return 3
	// 霾、雾
	case ic == 5, ic == 10, ic == 11, ic == 37:
		return 45
	// 阵雨、零星小雨
	case ic == 12, ic == 13, ic == 39, ic == 40, ic == 36:
		return 51
	// 雷阵雨
	case ic == 15, ic == 16, ic == 17, ic == 41, ic == 42:
		return 95
	// 雨
	case ic == 18, ic == 19, ic == 20, ic == 21, ic == 43, ic == 44:
		return 61
	// 雪、阵雪、吹雪、冻雨
	case ic == 22, ic == 23, ic == 24, ic == 25, ic == 26, ic == 27,
		ic == 28, ic == 29, ic == 38, ic == 46, ic == 47:
		return 71
	default:
		return 3
	}
}
