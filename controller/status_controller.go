package controller

import (
	"encoding/json"
	"net/http"

	"github.com/example/tracepulse/logger"
	"github.com/example/tracepulse/service"
	"go.uber.org/zap"
)

// StatusController 系统状态相关的 HTTP 请求处理器。
type StatusController struct {
	service service.StatusService // 系统状态业务逻辑实例
}

// NewStatusController 创建一个新的状态控制器实例。
func NewStatusController(service service.StatusService) *StatusController {
	return &StatusController{service: service}
}

// GetStatus 处理 GET /status 请求，返回系统运行状态。
func (c *StatusController) GetStatus(w http.ResponseWriter, r *http.Request) {
	status, err := c.service.GetStatus()
	if err != nil {
		logger.Error("get status failed", zap.Error(err))
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	logger.Debug("system status queried",
		zap.String("remote_addr", r.RemoteAddr),
	)

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(status)
}
