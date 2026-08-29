package controller

import (
	"encoding/json"
	"net/http"

	"github.com/example/gapi/service"
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
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(status)
}
