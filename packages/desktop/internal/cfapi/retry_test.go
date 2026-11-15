package cfapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// 前两次连接直接被掐断（模拟 EOF / 链路抖动），第三次正常：GET 应自动重试成功。
func TestGetRetriesTransientEOF(t *testing.T) {
	var n int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&n, 1) <= 2 {
			hj, _ := w.(http.Hijacker)
			conn, _, _ := hj.Hijack()
			_ = conn.Close()
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"success":true,"errors":[],"result":{"ok":1}}`))
	}))
	defer srv.Close()

	c := New(Options{Token: "t", APIBase: srv.URL, TimeoutMs: 5000})
	var out struct{ Ok int }
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := c.doJSON(ctx, http.MethodGet, "/x", nil, &out); err != nil {
		t.Fatalf("应重试成功，却失败：%v", err)
	}
	if out.Ok != 1 || atomic.LoadInt32(&n) != 3 {
		t.Fatalf("out=%+v 请求次数=%d，期望 ok=1、3 次", out, n)
	}
}

// 业务错误（HTTP 400）不能重试。
func TestBusinessErrorNotRetried(t *testing.T) {
	var n int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&n, 1)
		w.WriteHeader(400)
		_, _ = w.Write([]byte(`{"success":false,"errors":[{"code":1000,"message":"bad"}]}`))
	}))
	defer srv.Close()

	c := New(Options{Token: "t", APIBase: srv.URL})
	if err := c.doJSON(context.Background(), http.MethodGet, "/x", nil, nil); err == nil {
		t.Fatal("期望返回错误")
	}
	if atomic.LoadInt32(&n) != 1 {
		t.Fatalf("业务错误被重试了 %d 次", n)
	}
}

// POST 请求发出后连接被掐断：可能已被服务端处理，不能重试（避免重复提交）。
func TestPostNotRetriedAfterSend(t *testing.T) {
	var n int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&n, 1)
		hj, _ := w.(http.Hijacker)
		conn, _, _ := hj.Hijack()
		_ = conn.Close()
	}))
	defer srv.Close()

	c := New(Options{Token: "t", APIBase: srv.URL})
	if err := c.doJSON(context.Background(), http.MethodPost, "/x", map[string]string{"a": "b"}, nil); err == nil {
		t.Fatal("期望返回错误")
	}
	if atomic.LoadInt32(&n) != 1 {
		t.Fatalf("POST 发出后失败被重试了 %d 次", n)
	}
}
