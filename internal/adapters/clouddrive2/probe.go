package clouddrive2

import (
	"context"
	"strings"

	"bosstransfer/internal/adapters"
)

type Probe struct {
	Addr string
}

func (p Probe) Probe(ctx context.Context) adapters.ProbeResult {
	addr := strings.TrimSpace(p.Addr)
	if addr == "" {
		return adapters.ProbeResult{Service: "clouddrive2", OK: false, Message: "CloudDrive2 地址不能为空"}
	}
	client, err := NewClient(addr)
	if err != nil {
		return adapters.ProbeResult{Service: "clouddrive2", OK: false, Message: err.Error()}
	}
	defer client.Close()

	info, err := client.GetSystemInfo(ctx)
	if err != nil {
		return adapters.ProbeResult{Service: "clouddrive2", OK: false, Message: err.Error()}
	}
	message := "CloudDrive2 服务可用"
	if !info.IsLogin {
		message = "CloudDrive2 服务可用，但尚未登录"
	} else if !info.SystemReady {
		message = "CloudDrive2 已登录，但系统尚未就绪"
	}
	return adapters.ProbeResult{
		Service: "clouddrive2",
		OK:      true,
		Message: message,
	}
}
