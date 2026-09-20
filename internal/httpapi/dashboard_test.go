package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDashboardHandlerRendersPage(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()

	DashboardHandler(DashboardData{
		Title:      "BossTransfer 测试页",
		Role:       "页面渲染测试",
		Version:    "test",
		ListenAddr: "127.0.0.1:0",
		Cards: []DashboardCard{
			{Title: "状态", Value: "ok"},
		},
		Steps: []DashboardStep{
			{Title: "打开页面", Body: "确认可以渲染。"},
		},
		Links: []DashboardLink{
			{Label: "健康检查", Href: "/api/v1/health/live"},
		},
	})(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "BossTransfer 测试页") {
		t.Fatalf("page did not include title: %s", body)
	}
	if !strings.Contains(body, "/api/v1/health/ready") {
		t.Fatalf("page did not include default ready path")
	}
}

func TestDashboardHandlerRejectsUnknownPath(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/missing", nil)
	rec := httptest.NewRecorder()

	DashboardHandler(DashboardData{Title: "BossTransfer"})(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}
