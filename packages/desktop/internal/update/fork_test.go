package update

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestForkCheck(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/download/kwrt-latest/VERSION-windows" {
			_, _ = w.Write([]byte("0.50.0+bbb\n"))
			return
		}
		http.NotFound(w, r)
	}))
	defer ts.Close()
	oldBase, oldWeb := ForkBase, ReleasesWebBase
	defer func() { ForkBase, ReleasesWebBase = oldBase, oldWeb }()
	for _, k := range []string{"HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "all_proxy"} {
		t.Setenv(k, "")
	}
	UseFork(ts.URL)

	res := Check(context.Background(), "0.50.0+aaa")
	if res.Error != "" || !res.HasUpdate || res.Latest != "0.50.0+bbb" {
		t.Fatalf("应发现新版：%+v", res)
	}
	if res.DownloadURL != ts.URL+"/download/kwrt-latest/cfpilot-windows-amd64.exe" {
		t.Fatalf("下载地址不对：%s", res.DownloadURL)
	}
	if res := Check(context.Background(), "0.50.0+bbb"); res.HasUpdate {
		t.Fatalf("相同版本不应提示更新：%+v", res)
	}
}
