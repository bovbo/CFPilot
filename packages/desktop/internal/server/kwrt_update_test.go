package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fakeCtl(t *testing.T, body string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "cfpilot-ctl")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	old := kwrtCtlPath
	kwrtCtlPath = p
	t.Cleanup(func() { kwrtCtlPath = old })
}

func TestKwrtUnsupported(t *testing.T) {
	old := kwrtCtlPath
	kwrtCtlPath = "/nonexistent/ctl"
	defer func() { kwrtCtlPath = old }()
	w := httptest.NewRecorder()
	(&Server{}).handleKwrtCheck(w, httptest.NewRequest("GET", "/", nil))
	if !strings.Contains(w.Body.String(), `"supported":false`) {
		t.Fatal(w.Body.String())
	}
}

func TestKwrtCheckAndStatus(t *testing.T) {
	fakeCtl(t, `case "$1" in
check) [ "$2" = "--json" ] || exit 1; echo "检查中"; echo '{"current":"0.43.0+aaa","latest":"0.43.0+bbb","update":true,"error":""}';;
upgrade-status) echo "ok 123"; echo "line1";;
esac`)
	w := httptest.NewRecorder()
	(&Server{}).handleKwrtCheck(w, httptest.NewRequest("GET", "/", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"update":true`) || !strings.Contains(w.Body.String(), `"supported":true`) {
		t.Fatal(w.Body.String())
	}
	w = httptest.NewRecorder()
	(&Server{}).handleKwrtStatus(w, httptest.NewRequest("GET", "/", nil))
	if !strings.Contains(w.Body.String(), `"state":"ok"`) || !strings.Contains(w.Body.String(), "line1") {
		t.Fatal(w.Body.String())
	}
}

func TestKwrtBlocksDesktopUpdate(t *testing.T) {
	fakeCtl(t, "exit 0\n")
	s := &Server{}
	w := httptest.NewRecorder()
	s.handleUpdateCheck(w, httptest.NewRequest("POST", "/api/update/check", nil))
	if !strings.Contains(w.Body.String(), `"hasUpdate":false`) || !strings.Contains(w.Body.String(), "一键更新") {
		t.Fatal(w.Body.String())
	}
	w = httptest.NewRecorder()
	s.handleUpdateDownload(w, httptest.NewRequest("POST", "/api/update/download", strings.NewReader(`{"url":"https://x"}`)))
	if w.Code != http.StatusBadRequest {
		t.Fatal(w.Code, w.Body.String())
	}
	w = httptest.NewRecorder()
	s.handleUpdateApply(w, httptest.NewRequest("POST", "/api/update/apply", strings.NewReader(`{"file":"x"}`)))
	if w.Code != http.StatusBadRequest {
		t.Fatal(w.Code, w.Body.String())
	}
}
